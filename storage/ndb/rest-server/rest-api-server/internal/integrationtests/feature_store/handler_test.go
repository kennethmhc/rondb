/*
 * This file is part of the RonDB REST API Server
 * Copyright (c) 2023 Hopsworks AB
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, version 3.
 *
 * This program is distributed in the hope that it will be useful, but
 * WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
 * General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program. If not, see <http://www.gnu.org/licenses/>.
 */

package feature_store

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"hopsworks.ai/rdrs/internal/config"
	"hopsworks.ai/rdrs/internal/integrationtests/testclient"
	"hopsworks.ai/rdrs/internal/log"
	"hopsworks.ai/rdrs/internal/testutils"
	"hopsworks.ai/rdrs/pkg/api"
)

func TestFeatureStore(t *testing.T) {
	var fvName = "adb"
	var fvVersion = 0
	key := string("id1")
	value1 := json.RawMessage(`"12"`)
	value2 := json.RawMessage(`"2022-01-09"`)
	var entries = make(map[string]*json.RawMessage)
	entries[key] = &value1
	entries[string("fg2_id1")] = &value2
	var passedFeatures = make(map[string]*json.RawMessage)
	pfValue := json.RawMessage(`999`)
	passedFeatures["data1"] = &pfValue
	req := api.FeatureStoreRequest{FeatureViewName: &fvName, FeatureViewVersion: &fvVersion, Entries: &entries, PassedFeatures: &passedFeatures}
	reqBody := fmt.Sprintf("%s", req)
	log.Debugf("Request body: %s", reqBody)
	_, respBody := testclient.SendHttpRequest(t, config.FEATURE_STORE_HTTP_VERB, testutils.NewFeatureStoreURL(), reqBody, "", http.StatusOK)

	fsResp := api.FeatureStoreResponse{}
	fmt.Printf("response body: %s", respBody)
	err := json.Unmarshal([]byte(respBody), &fsResp)
	if err != nil {
		t.Fatalf("Unmarshal failed %s ", err)
	}

	log.Infof("Response data is %s", fsResp.String())
}
