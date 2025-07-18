// Copyright 2023 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package job

import (
	"context"
	"reflect"

	"github.com/pkg/errors"
	"github.com/tsuru/tsuru/action"
	"github.com/tsuru/tsuru/auth"
	"github.com/tsuru/tsuru/db/storagev2"
	"github.com/tsuru/tsuru/log"
	"github.com/tsuru/tsuru/servicemanager"
	authTypes "github.com/tsuru/tsuru/types/auth"
	jobTypes "github.com/tsuru/tsuru/types/job"
	mongoBSON "go.mongodb.org/mongo-driver/bson"
)

var provisionJob = action.Action{
	Name: "provision-job",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		var job *jobTypes.Job
		switch ctx.Params[0].(type) {
		case *jobTypes.Job:
			job = ctx.Params[0].(*jobTypes.Job)
		default:
			return nil, errors.New("first parameter must be *Job")
		}
		prov, err := getProvisioner(ctx.Context, job)
		if err != nil {
			return nil, err
		}
		err = prov.EnsureJob(ctx.Context, job)
		return nil, err
	},
	Backward: func(ctx action.BWContext) {
		var job *jobTypes.Job
		switch ctx.Params[0].(type) {
		case *jobTypes.Job:
			job = ctx.Params[0].(*jobTypes.Job)
		default:
			return
		}
		prov, err := getProvisioner(ctx.Context, job)
		if err == nil {
			prov.DestroyJob(ctx.Context, job)
		}
	},
	MinParams: 1,
}

var triggerCron = action.Action{
	Name: "trigger-cronjob",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		var job *jobTypes.Job
		switch ctx.Params[0].(type) {
		case *jobTypes.Job:
			job = ctx.Params[0].(*jobTypes.Job)
		default:
			return nil, errors.New("first parameter must be *Job")
		}
		prov, err := getProvisioner(ctx.Context, job)
		if err != nil {
			return nil, err
		}
		return nil, prov.TriggerCron(ctx.Context, job, job.Pool)
	},
	MinParams: 1,
}

var updateJobProv = action.Action{
	Name: "update-job",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		var job *jobTypes.Job
		switch ctx.Params[0].(type) {
		case *jobTypes.Job:
			job = ctx.Params[0].(*jobTypes.Job)
		default:
			return nil, errors.New("first parameter must be *Job")
		}
		return nil, servicemanager.Job.UpdateJobProv(ctx.Context, job)
	},
	MinParams: 1,
}

// updateJob is an action that updates a job in the database in Forward and
// does nothing in the Backward.
//
// The first argument in the context must be a Job or a pointer to a Job.
var jobUpdateDB = action.Action{
	Name: "update-job-db",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		var j *jobTypes.Job
		switch ctx.Params[0].(type) {
		case *jobTypes.Job:
			j = ctx.Params[0].(*jobTypes.Job)
		default:
			return nil, errors.New("first parameter must be *Job")
		}

		oldJob, err := servicemanager.Job.GetByName(ctx.Context, j.Name)
		if err != nil {
			return nil, updateJobDB(ctx.Context, j)
		}

		return oldJob, updateJobDB(ctx.Context, j)
	},
	Backward: func(ctx action.BWContext) {
		if ctx.FWResult == nil {
			return
		}
		oldJob, ok := ctx.FWResult.(*jobTypes.Job)
		if !ok {
			return
		}
		if err := updateJobDB(ctx.Context, oldJob); err != nil {
			log.Errorf("Error trying to rollback old job %s: %v", oldJob.Name, err)
		}
	},
	MinParams: 1,
}

// insertJob is an action that inserts a job in the database in Forward and
// removes it in the Backward.
// insert job must always be run after provision-job because it depends on
// the value of ctx.Previous
//
// The first argument in the context must be a Job or a pointer to a Job.
var insertJob = action.Action{
	Name: "insert-job",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		var j *jobTypes.Job
		switch ctx.Params[0].(type) {
		case *jobTypes.Job:
			j = ctx.Params[0].(*jobTypes.Job)
		default:
			return nil, errors.New("first parameter must be *Job")
		}
		err := insertJobDB(ctx.Context, j)
		if err != nil {
			return nil, err
		}
		return j, nil
	},
	Backward: func(ctx action.BWContext) {
		job := ctx.FWResult.(*jobTypes.Job)
		servicemanager.Job.RemoveJob(ctx.Context, job)
	},
	MinParams: 1,
}

func insertJobDB(ctx context.Context, job *jobTypes.Job) error {
	collection, err := storagev2.JobsCollection()
	if err != nil {
		return err
	}
	_, err = servicemanager.Job.GetByName(ctx, job.Name)
	if err == jobTypes.ErrJobNotFound {
		_, err = collection.InsertOne(ctx, job)
		return err
	} else if err == nil {
		return jobTypes.ErrJobAlreadyExists
	}
	return err
}

func updateJobDB(ctx context.Context, job *jobTypes.Job) error {
	collection, err := storagev2.JobsCollection()
	if err != nil {
		return err
	}

	oldJob, err := servicemanager.Job.GetByName(ctx, job.Name)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(*oldJob, *job) {
		return nil
	}

	_, err = collection.ReplaceOne(ctx, mongoBSON.M{"name": job.Name}, job)

	return err
}

var reserveTeamCronjob = action.Action{
	Name: "reserve-team-job",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		var job *jobTypes.Job
		switch ctx.Params[0].(type) {
		case *jobTypes.Job:
			job = ctx.Params[0].(*jobTypes.Job)
		default:
			return nil, errors.New("first parameter must be *Job")
		}
		if err := servicemanager.TeamQuota.Inc(ctx.Context, &authTypes.Team{Name: job.TeamOwner}, 1); err != nil {
			return nil, err
		}
		return map[string]string{"job": job.Name, "team": job.TeamOwner}, nil
	},
	Backward: func(ctx action.BWContext) {
		m := ctx.FWResult.(map[string]string)
		if teamStr, ok := m["team"]; ok {
			servicemanager.TeamQuota.Inc(ctx.Context, &authTypes.Team{Name: teamStr}, -1)
		}
	},
	MinParams: 2,
}

// reserveUserCronjob reserves the job for the user, only if the user has a quota
// of jobs. If the user does not have a quota, meaning that it's unlimited,
// reserveUserCronjob.Forward just returns nil.
var reserveUserCronjob = action.Action{
	Name: "reserve-user-cronjob",
	Forward: func(ctx action.FWContext) (action.Result, error) {
		var job *jobTypes.Job
		switch ctx.Params[0].(type) {
		case *jobTypes.Job:
			job = ctx.Params[0].(*jobTypes.Job)
		default:
			return nil, errors.New("first parameter must be *Job")
		}
		var user authTypes.User
		switch ctx.Params[1].(type) {
		case authTypes.User:
			user = ctx.Params[1].(authTypes.User)
		case *authTypes.User:
			user = *ctx.Params[1].(*authTypes.User)
		default:
			return nil, errors.New("second parameter must be auth.User or *auth.User")
		}
		if user.FromToken {
			// there's no quota to update as the user was generated from team token.
			return map[string]string{"job": job.Name}, nil
		}
		u := auth.User(user)
		if err := servicemanager.UserQuota.Inc(ctx.Context, &u, 1); err != nil {
			return nil, err
		}
		return map[string]string{"job": job.Name, "user": user.Email}, nil
	},
	Backward: func(ctx action.BWContext) {
		m, found := ctx.FWResult.(map[string]string)
		if !found {
			return
		}
		email, found := m["user"]
		if !found {
			return
		}
		if user, err := auth.GetUserByEmail(ctx.Context, email); err == nil {
			servicemanager.UserQuota.Inc(ctx.Context, user, -1)
		}
	},
	MinParams: 2,
}
