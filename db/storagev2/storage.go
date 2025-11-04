// Copyright 2024 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package storagev2

import (
	"context"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
	"github.com/tsuru/config"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsoncodec"
	"go.mongodb.org/mongo-driver/bson/bsonrw"
	"go.mongodb.org/mongo-driver/mongo"
	"k8s.io/apimachinery/pkg/runtime"

	"go.mongodb.org/mongo-driver/mongo/options"

	mongoprom "github.com/globocom/mongo-go-prometheus"
	appTypes "github.com/tsuru/tsuru/types/app"
)

const (
	DefaultDatabaseURL  = "mongodb://127.0.0.1:27017"
	DefaultDatabaseName = "tsuru"

	// Connection timeouts
	defaultConnectTimeout         = 30 * time.Second
	defaultServerSelectionTimeout = 10 * time.Second
	defaultSocketTimeout          = 30 * time.Second
	defaultPingTimeout            = 5 * time.Second

	// Connection pool settings
	defaultMinPoolSize   = 2
	defaultMaxPoolSize   = 100
	defaultMaxConnecting = 10
)

var (
	client          atomic.Pointer[mongo.Client]
	databaseNamePtr atomic.Pointer[string]
)

func init() {
	var mapRuntimeObject map[string]runtime.Object
	var runtimeObject runtime.Object

	var ignoreEncode bsoncodec.ValueEncoderFunc = func(ec bsoncodec.EncodeContext, vw bsonrw.ValueWriter, val reflect.Value) error {
		vw.WriteUndefined()
		return nil
	}

	var ignoreDecode bsoncodec.ValueDecoderFunc = func(dc bsoncodec.DecodeContext, vr bsonrw.ValueReader, val reflect.Value) error {
		vr.Skip()
		return nil
	}

	var certIssuersEncode bsoncodec.ValueEncoderFunc = func(ec bsoncodec.EncodeContext, vw bsonrw.ValueWriter, val reflect.Value) error {
		documentWriter, err := vw.WriteDocument()
		if err != nil {
			return err
		}

		iter := val.MapRange()

		for iter.Next() {
			key := iter.Key().String()
			value := iter.Value().String()
			escapedKey := strings.ReplaceAll(key, ".", appTypes.CertIssuerDotReplacement)

			valueWriter, err := documentWriter.WriteDocumentElement(escapedKey)
			if err != nil {
				return err
			}

			valueWriter.WriteString(value)

		}

		documentWriter.WriteDocumentEnd()

		return nil
	}

	var certIssuersDecode bsoncodec.ValueDecoderFunc = func(dc bsoncodec.DecodeContext, vr bsonrw.ValueReader, val reflect.Value) error {
		if vr.Type() != bson.TypeEmbeddedDocument {
			vr.Skip()
			return nil
		}
		documentReader, err := vr.ReadDocument()
		if err != nil {
			return err
		}

		val.Set(reflect.ValueOf(appTypes.CertIssuers{}))

		for {

			key, valueReader, err := documentReader.ReadElement()
			if err == bsonrw.ErrEOD {
				break
			}
			if err != nil {
				return err
			}

			value, err := valueReader.ReadString()
			if err != nil {
				return err
			}
			unescappedKey := strings.ReplaceAll(key, appTypes.CertIssuerDotReplacement, ".")
			val.SetMapIndex(reflect.ValueOf(unescappedKey), reflect.ValueOf(value))
		}

		return nil
	}

	bson.DefaultRegistry.RegisterTypeEncoder(reflect.TypeOf(&mapRuntimeObject).Elem(), ignoreEncode)
	bson.DefaultRegistry.RegisterTypeEncoder(reflect.TypeOf(&runtimeObject).Elem(), ignoreEncode)
	bson.DefaultRegistry.RegisterTypeDecoder(reflect.TypeOf(&mapRuntimeObject).Elem(), ignoreDecode)
	bson.DefaultRegistry.RegisterTypeDecoder(reflect.TypeOf(&runtimeObject).Elem(), ignoreDecode)

	bson.DefaultRegistry.RegisterTypeEncoder(reflect.TypeOf(appTypes.CertIssuers{}), certIssuersEncode)
	bson.DefaultRegistry.RegisterTypeDecoder(reflect.TypeOf(appTypes.CertIssuers{}), certIssuersDecode)
}

func Reset() {
	client.Store(nil)
	databaseNamePtr.Store(nil)
}

var monitor = mongoprom.NewCommandMonitor(
	mongoprom.WithInstanceName("tsurud"),
	mongoprom.WithNamespace("tsuru"),
	mongoprom.WithDurationBuckets([]float64{.001, .005, .01, .05, .1, .5, 1, 5, 10}),
)

func connect() (*mongo.Client, *string, error) {
	var uri string

	ctx, cancel := context.WithTimeout(context.Background(), defaultConnectTimeout)
	defer cancel()

	uri, databaseName := dbConfig()

	clientOpts := options.Client().
		ApplyURI(uri).
		SetAppName("tsurud").
		SetBSONOptions(&options.BSONOptions{
			NilSliceAsEmpty: true,
			NilMapAsEmpty:   true,
		}).
		SetMonitor(monitor).
		SetServerSelectionTimeout(defaultServerSelectionTimeout).
		SetConnectTimeout(defaultServerSelectionTimeout).
		SetSocketTimeout(defaultSocketTimeout).
		SetMaxConnecting(uint64(defaultMaxConnecting)).
		SetMinPoolSize(uint64(defaultMinPoolSize)).
		SetMaxPoolSize(uint64(defaultMaxPoolSize)).
		SetRetryWrites(true).
		SetRetryReads(true)

	connectedClient, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		return nil, nil, err
	}

	// Verify the connection works
	pingCtx, pingCancel := context.WithTimeout(context.Background(), defaultPingTimeout)
	defer pingCancel()
	err = connectedClient.Ping(pingCtx, nil)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to ping mongodb")
	}

	swapped := client.CompareAndSwap(nil, connectedClient)
	databaseNamePtr.Store(&databaseName)

	if swapped {
		err = EnsureIndexesCreated(connectedClient.Database(databaseName))

		if err != nil {
			return nil, nil, errors.Wrap(err, "failed to create indexes")
		}
	}

	return connectedClient, &databaseName, nil
}

func dbConfig() (string, string) {
	uri, _ := config.GetString("database:url")
	if uri == "" {
		uri = DefaultDatabaseURL
	}

	if !strings.HasPrefix(uri, "mongodb://") {
		uri = "mongodb://" + uri
	}

	uriParsed, _ := url.Parse(uri)

	if uriParsed.Path == "" {
		uriParsed.Path = "/"
	}

	// Add default connection parameters if not present in the URL
	// These defaults help with connection stability across different platforms
	// (especially Docker Desktop on macOS where network virtualization adds latency)
	query := uriParsed.Query()
	if !query.Has("maxPoolSize") {
		query.Set("maxPoolSize", strconv.Itoa(defaultMaxPoolSize))
	}
	if !query.Has("minPoolSize") {
		query.Set("minPoolSize", strconv.Itoa(defaultMinPoolSize))
	}
	if !query.Has("serverSelectionTimeoutMS") {
		query.Set("serverSelectionTimeoutMS", strconv.FormatInt(defaultServerSelectionTimeout.Milliseconds(), 10))
	}
	if !query.Has("connectTimeoutMS") {
		query.Set("connectTimeoutMS", strconv.FormatInt(defaultServerSelectionTimeout.Milliseconds(), 10))
	}
	if !query.Has("socketTimeoutMS") {
		query.Set("socketTimeoutMS", strconv.FormatInt(defaultSocketTimeout.Milliseconds(), 10))
	}
	if !query.Has("retryWrites") {
		query.Set("retryWrites", "true")
	}
	if !query.Has("retryReads") {
		query.Set("retryReads", "true")
	}
	uriParsed.RawQuery = query.Encode()

	dbname, _ := config.GetString("database:name")
	if dbname == "" {
		dbname = DefaultDatabaseName
	}

	return uriParsed.String(), dbname
}

func Collection(name string) (*mongo.Collection, error) {
	db, err := database()
	if err != nil {
		return nil, err
	}

	return db.Collection(name, options.Collection()), nil
}

func database() (*mongo.Database, error) {
	connectedClient := client.Load()
	databaseName := databaseNamePtr.Load()

	if connectedClient == nil || databaseName == nil {
		var err error
		connectedClient, databaseName, err = connect()
		if err != nil {
			return nil, err
		}
	}
	return connectedClient.Database(*databaseName), nil
}
