// Copyright 2018 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package kubernetes

import (
	"context"

	"github.com/tsuru/tsuru/servicemanager"
	appTypes "github.com/tsuru/tsuru/types/app"
	check "gopkg.in/check.v1"
)

func newEmptyVersion(c *check.C, app *appTypes.App) appTypes.AppVersion {
	version, err := servicemanager.AppVersion.NewAppVersion(context.TODO(), appTypes.NewVersionArgs{
		App: app,
	})
	c.Assert(err, check.IsNil)
	return version
}

func newVersion(c *check.C, app *appTypes.App, processes map[string][]string, customData ...map[string]interface{}) appTypes.AppVersion {
	version := newEmptyVersion(c, app)
	err := version.CommitBuildImage()
	c.Assert(err, check.IsNil)

	args := appTypes.AddVersionDataArgs{
		Processes: processes,
	}

	if len(customData) > 0 {
		args.CustomData = customData[0]
	}

	err = version.AddData(args)
	c.Assert(err, check.IsNil)
	return version
}

func newCommittedVersion(c *check.C, app *appTypes.App, processes map[string][]string, customData ...map[string]interface{}) appTypes.AppVersion {
	version := newVersion(c, app, processes, customData...)
	err := version.CommitBaseImage()
	c.Assert(err, check.IsNil)
	return version
}

func newSuccessfulVersion(c *check.C, app *appTypes.App, processes map[string][]string, customData ...map[string]interface{}) appTypes.AppVersion {
	version := newCommittedVersion(c, app, processes, customData...)
	err := version.CommitSuccessful()
	c.Assert(err, check.IsNil)
	return version
}
