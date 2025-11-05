// Copyright 2025 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package integration

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/tsuru/tsuru/types/app"

	check "gopkg.in/check.v1"
)

func multiversionStopStartTest() ExecFlow {
	flow := ExecFlow{
		matrix: map[string]string{
			"pool": "poolnames",
		},
		parallel: false, // Run sequentially to avoid conflicts
		requires: []string{"team", "poolnames", "installedplatforms"},
	}

	flow.forward = func(c *check.C, env *Environment) {
		cwd, err := os.Getwd()
		c.Assert(err, check.IsNil)
		// Use the new multiversion-python-app fixture
		appDir := path.Join(cwd, "fixtures", "multiprocess-python-app")
		appName := slugifyName(fmt.Sprintf("mv-stop-start-%s", env.Get("pool")))

		// Create the test application
		res := T("app", "create", appName, "python-iplat", "-t", "{{.team}}", "-o", "{{.pool}}").Run(env)
		c.Assert(res, ResultOk)

		// Map to track image -> hash relationship
		imageToHash := make(map[string]string)

		// Step 1: Deploy initial version (version 1)
		hash1 := deployAndMapHash(c, appDir, appName, []string{}, imageToHash, env)
		checkAppHealth(c, appName, "1", hash1, env)

		// Step 2: Deploy second version (version 2) with --new-version to create multiversion scenario
		hash2 := deployAndMapHash(c, appDir, appName, []string{"--new-version"}, imageToHash, env)
		checkAppHealth(c, appName, "2", hash2, env)

		// Step 3: Add version 3 to router to create true multiversion deployment
		res, ok := T("app", "router", "version", "add", "2", "-a", appName).Retry(time.Minute, env)
		c.Assert(res, ResultOk)
		c.Assert(ok, check.Equals, true)

		// Verify multiversion is working - should see both version 1 and 2
		appInfoMulti := checkAppHealth(c, appName, "2", hash2, env)
		routerAddrMulti := appInfoMulti.Routers[0].Address
		cmd := NewCommand("curl", "-m5", "-sSf", "http://"+routerAddrMulti)
		hashRE := regexp.MustCompile(`.* version: (\d+) - hash: (\w+)$`)

		// Test multiple requests to ensure we hit both versions
		verifyVersionHashes(c, map[string]string{
			"1": hash1,
			"2": hash2,
		}, cmd, hashRE, env)

		// Step 4: Unit add to version 1 process web
		res = T("app", "unit", "add", "1", "-a", appName, "-p", "web", "--version", "1").Run(env)
		c.Assert(res, ResultOk)

		appInfo, ok := checkAppExternallyAddressable(c, appName, env)
		c.Assert(ok, check.Equals, true)
		checkAppUnits(c, appInfo, "web", 1, 2, env)

		// Step 5: Unit add to version 1 process web2
		res = T("app", "unit", "add", "1", "-a", appName, "-p", "web2", "--version", "1").Run(env)
		c.Assert(res, ResultOk)

		appInfo, ok = checkAppExternallyAddressable(c, appName, env)
		c.Assert(ok, check.Equals, true)
		checkAppUnits(c, appInfo, "web2", 1, 2, env)

		// Step 6: Unit add to version 1 process web3
		res = T("app", "unit", "add", "1", "-a", appName, "-p", "web3", "--version", "1").Run(env)
		c.Assert(res, ResultOk)

		appInfo, ok = checkAppExternallyAddressable(c, appName, env)
		c.Assert(ok, check.Equals, true)
		checkAppUnits(c, appInfo, "web3", 1, 2, env)

		// Step 7: app stop
		res = T("app", "stop", "-a", appName).Run(env)
		c.Assert(res, ResultOk)

		// Wait k8s sync
		checkAppStopped(c, appName, env)

		// Step 8: app start
		res = T("app", "start", "-a", appName).Run(env)
		c.Assert(res, ResultOk)
		_, ok = checkAppExternallyAddressable(c, appName, env)
		c.Assert(ok, check.Equals, true)

		appInfo = checkAppHealth(c, appName, "1", hash1, env)
		checkAppUnits(c, appInfo, "web", 1, 2, env)
		checkAppUnits(c, appInfo, "web2", 1, 2, env)
		checkAppUnits(c, appInfo, "web3", 1, 2, env)
	}

	flow.backward = func(c *check.C, env *Environment) {
		appName := slugifyName(fmt.Sprintf("mv-rollback-%s", env.Get("pool")))
		res := T("app", "remove", "-y", "-a", appName).Run(env)
		c.Check(res, ResultOk)
	}

	return flow
}

func checkAppUnits(c *check.C, app *app.AppInfo, expectedProcess string, expectedVersion, numberOfUnits int, env *Environment) {
	filteredUnitCount := 0
	for _, unit := range app.Units {
		if unit.ProcessName == expectedProcess && unit.Version == expectedVersion {
			filteredUnitCount++
		}
	}
	c.Assert(filteredUnitCount, check.Equals, numberOfUnits, check.Commentf("Expected %d units for process %s version %d, but found %d", numberOfUnits, expectedProcess, expectedVersion, filteredUnitCount))
}

func checkAppStopped(c *check.C, appName string, env *Environment) {
	ok := retry(3*time.Minute, func() (ready bool) {
		res := K("get", "pods", "-l", fmt.Sprintf("tsuru.io/app-name=%s", appName)).Run(env)
		c.Assert(res, ResultOk)
		podList := strings.Split(strings.TrimSpace(res.Stdout.String()), "\n")
		return len(podList) == 0
	})
	c.Assert(ok, check.Equals, true, check.Commentf("App %s did not stop properly", appName))
}
