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
	"regexp"
	"strings"

	"hopsworks.ai/rdrs/internal/config"
	"hopsworks.ai/rdrs/internal/feature_store"
	"hopsworks.ai/rdrs/internal/handlers/batchpkread"
	"hopsworks.ai/rdrs/internal/log"
	"hopsworks.ai/rdrs/internal/security/apikey"
	"hopsworks.ai/rdrs/pkg/api"
)

const (
	JSON_NUMBER  = "NUMBER"
	JSON_STRING  = "STRING"
	JSON_BOOLEAN = "BOOLEAN"
	JSON_NIL     = "NIL"
	JSON_OTHER   = "OTHER"
)

type Handler struct {
	fvMetaCache   *feature_store.FeatureViewMetaDataCache
	apiKeyCache   apikey.Cache
	dbBatchReader batchpkread.Handler
}

func New(fvMetaCache *feature_store.FeatureViewMetaDataCache, apiKeyCache apikey.Cache, batchPkReadHandler batchpkread.Handler) Handler {
	return Handler{fvMetaCache, apiKeyCache, batchPkReadHandler}
}

func (h *Handler) Validate(request interface{}) error {
	fsReq := request.(*api.FeatureStoreRequest)
	metadata, err := h.fvMetaCache.Get(
		*fsReq.FeatureStoreName, *fsReq.FeatureViewName, *fsReq.FeatureViewVersion)
	if err != nil {
		return err.GetError()
	}
	if log.IsDebug() {
		log.Debugf("Feature store request is %s", fsReq.String())
	}
	err1 := ValidatePrimaryKey(fsReq.Entries, &metadata.PrefixFeaturesLookup)
	if err1 != nil {
		return err1.GetError()
	}
	err2 := ValidatePassedFeatures(fsReq.PassedFeatures, &metadata.PrefixFeaturesLookup)
	if err2 != nil {
		return err2.GetError()
	}
	return nil

}

func ValidatePrimaryKey(entries *map[string]*json.RawMessage, features *map[string]*feature_store.FeatureMetadata) *feature_store.RestErrorCode {
	// Data type check of primary key will be delegated to rondb.
	if len(*entries) == 0 {
		return feature_store.INCORRECT_PRIMARY_KEY
	}
	for featureName := range *entries {
		_, ok := (*features)[featureName]
		if !ok {
			return feature_store.FEATURE_NOT_EXIST.NewMessage(fmt.Sprintf("Provided primary key `%s` does not exist in the feature view.", featureName))
		}
	}
	return nil
}

func ValidatePassedFeatures(passedFeatures *map[string]*json.RawMessage, features *map[string]*feature_store.FeatureMetadata) *feature_store.RestErrorCode {
	if passedFeatures == nil {
		return nil
	}
	for featureName, value := range *passedFeatures {
		feature, ok := (*features)[featureName]
		if !ok {
			return feature_store.FEATURE_NOT_EXIST.NewMessage(fmt.Sprintf("Feature `%s` does not exist in the feature view.", featureName))
		}
		err := ValidateFeatureType(value, feature.Type)
		if err != nil {
			return err
		}
	}
	return nil
}

func ValidateFeatureType(feature *json.RawMessage, featureType string) *feature_store.RestErrorCode {
	var got, err = getJsonType(feature)

	if err != nil {
		return feature_store.INCORRECT_FEATURE_VALUE.NewMessage(fmt.Sprintf("Provided value %s is not in correct JSON format. %s", feature, err))
	}
	var expected = mapFeatureTypeToJsonType(featureType)
	if got != expected {
		return feature_store.WRONG_DATA_TYPE.NewMessage(fmt.Sprintf("Got: '%s', expected: '%s' (offline type: %s)", got, expected, featureType))
	}
	return nil
}

func mapFeatureTypeToJsonType(featureType string) string {
	switch featureType {
	case "boolean":
		return JSON_BOOLEAN
	case "tinyint":
		return JSON_NUMBER
	case "int":
		return JSON_NUMBER
	case "smallint":
		return JSON_NUMBER
	case "bigint":
		return JSON_NUMBER
	case "float":
		return JSON_NUMBER
	case "double":
		return JSON_NUMBER
	case "decimal":
		return JSON_NUMBER
	case "timestamp":
		return JSON_NUMBER
	case "date":
		return JSON_STRING
	case "string":
		return JSON_STRING
	case "binary":
		return JSON_STRING
	default:
		return JSON_OTHER
	}
}

func getJsonType(jsonString *json.RawMessage) (string, error) {
	var value interface{}
	if err := json.Unmarshal(*jsonString, &value); err != nil {
		return "", err
	}

	switch value.(type) {
	case float64:
		return JSON_NUMBER, nil
	case string:
		return JSON_STRING, nil
	case bool:
		return JSON_BOOLEAN, nil
	case nil:
		return JSON_NIL, nil
	default:
		return JSON_OTHER, nil
	}
}

func (h *Handler) Authenticate(apiKey *string, request interface{}) error {
	conf := config.GetAll()
	if !conf.Security.APIKey.UseHopsworksAPIKeys {
		return nil
	}
	fsReq := request.(*api.FeatureStoreRequest)
	metadata, err := h.fvMetaCache.Get(
		*fsReq.FeatureStoreName, *fsReq.FeatureViewName, *fsReq.FeatureViewVersion)
	if err != nil {
		return err.GetError()
	}
	valErr := h.apiKeyCache.ValidateAPIKey(apiKey, metadata.FeatureStoreNames...)
	if valErr != nil {
		return feature_store.FEATURE_STORE_NOT_SHARED.GetError()
	}
	return nil
}

func (h *Handler) Execute(request interface{}, response interface{}) (int, error) {

	fsReq := request.(*api.FeatureStoreRequest)
	metadata, err := h.fvMetaCache.Get(
		*fsReq.FeatureStoreName, *fsReq.FeatureViewName, *fsReq.FeatureViewVersion)
	if err != nil {
		return err.GetStatus(), err.GetError()
	}
	var readParams = GetBatchPkReadParams(metadata, fsReq.Entries)
	ronDbErr := h.dbBatchReader.Validate(readParams)
	if ronDbErr != nil {
		var fsError = TranslateRonDbError(http.StatusBadRequest, ronDbErr.Error())
		return fsError.GetStatus(), fsError.GetError()
	}
	var dbResponseIntf = getPkReadResponseJSON(*metadata)
	code, ronDbErr := h.dbBatchReader.Execute(readParams, *dbResponseIntf)
	if ronDbErr != nil {
		var fsError = TranslateRonDbError(code, ronDbErr.Error())
		return fsError.GetStatus(), fsError.GetError()
	}
	jsonResponse := (*dbResponseIntf).String()
	if log.IsDebug() {
		log.Debugf("Rondb response: code: %d, error: %s, body: %s", code, ronDbErr, jsonResponse)
	}
	rondbResp := api.BatchResponseJSON{}
	json.Unmarshal([]byte(jsonResponse), &rondbResp)
	fsError := checkRondbResponse(&rondbResp)
	if fsError != nil {
		return fsError.GetStatus(), fsError.GetError()
	}
	features, status := GetFeatureValues(rondbResp.Result, fsReq.Entries, metadata)
	fsResp := response.(*api.FeatureStoreResponse)
	fsResp.Status = status
	FillPassedFeatures(features, fsReq.PassedFeatures, &metadata.PrefixFeaturesLookup, &metadata.FeatureIndexLookup)
	fsResp.Features = *features
	if fsReq.MetadataRequest != nil {
		fsResp.Metadata = *GetFeatureMetadata(metadata, fsReq.MetadataRequest)
	}
	return http.StatusOK, nil
}

func checkRondbResponse(rondbResp *api.BatchResponseJSON) *feature_store.RestErrorCode {
	for _, result := range *rondbResp.Result {
		if *result.Code != 200 && *result.Code != 404 {
			return TranslateRonDbError(int(*result.Code), *result.Message)
		}
	}
	return nil
}

func GetFeatureMetadata(metadata *feature_store.FeatureViewMetadata, metaRequest *api.MetadataRequest) *[]*api.FeatureMeatadata {
	featureMetadataArray := make([]*api.FeatureMeatadata, metadata.NumOfFeatures)
	for featureKey, metadata := range metadata.PrefixFeaturesLookup {
		featureMetadata := api.FeatureMeatadata{}
		if metaRequest.FeatureName {
			var fk = featureKey
			featureMetadata.Name = &fk
		}
		if metaRequest.FeatureType {
			var ft = metadata.Type
			featureMetadata.Type = &ft
		}
		featureMetadataArray[metadata.Index] = &featureMetadata
	}
	return &featureMetadataArray
}

func TranslateRonDbError(code int, err string) *feature_store.RestErrorCode {
	var fsError *feature_store.RestErrorCode
	if strings.Contains(err, "Wrong data type.") {
		regex := regexp.MustCompile(`Expecting (\w+)\. Column: (\w+)`)
		match := regex.FindStringSubmatch(err)
		if match != nil {
			dataType := match[1]
			columnName := match[2]
			fsError = feature_store.WRONG_DATA_TYPE.NewMessage(
				fmt.Sprintf("Primary key '%s' should be in '%s' format.", columnName, dataType),
			)
		} else {
			fsError = feature_store.WRONG_DATA_TYPE
		}

	} else if strings.Contains(err, "Wrong number of primary-key columns.") ||
		strings.Contains(err, "Wrong primay-key column.") ||
		strings.Contains(err, "Column does not exist.") {
		fsError = feature_store.INCORRECT_PRIMARY_KEY.NewMessage(err)
	} else {
		if code == 400 {
			fsError = feature_store.READ_FROM_DB_FAIL_BAD_INPUT.NewMessage(err)
		} else {
			fsError = feature_store.READ_FROM_DB_FAIL
		}
	}
	return fsError
}

func GetFeatureValues(ronDbResult *[]*api.PKReadResponseWithCodeJSON, entries *map[string]*json.RawMessage, featureView *feature_store.FeatureViewMetadata) (*[]interface{}, api.FeatureStatus) {
	featureValues := make([]interface{}, featureView.NumOfFeatures)
	var status = api.FEATURE_STATUS_COMPLETE
	for _, response := range *ronDbResult {
		if *response.Code == 404 {
			status = api.FEATURE_STATUS_MISSING
		} else if *response.Code != 200 {
			status = api.FEATURE_STATUS_ERROR
		}
		for featureName, value := range *response.Body.Data {
			featureIndexKey := feature_store.GetFeatureIndexKeyByFgIndexKey(*response.Body.OperationID, featureName)
			if index, ok := (featureView.FeatureIndexLookup)[featureIndexKey]; ok {
				featureValues[index] = value
			} else {
				panic(fmt.Sprintf("Index cannot be found by the key '%s'", featureIndexKey))
			}
		}
	}
	// Fill in primary key value from request into the vector
	for featureName, value := range *entries {
		indexKey := feature_store.GetFeatureIndexKeyByFeature((featureView.PrefixFeaturesLookup)[featureName])
		if index, ok := (featureView.FeatureIndexLookup)[indexKey]; ok {
			featureValues[index] = value
		}
	}
	return &featureValues, status
}

func GetBatchPkReadParams(metadata *feature_store.FeatureViewMetadata, entries *map[string]*json.RawMessage) *[]*api.PKReadParams {

	var batchReadParams = make([]*api.PKReadParams, 0, 0)
	for _, fgFeature := range metadata.FeatureGroupFeatures {
		testDb := fgFeature.FeatureStoreName
		testTable := fmt.Sprintf("%s_%d", fgFeature.FeatureGroupName, fgFeature.FeatureGroupVersion)
		var filters = make([]api.Filter, 0, 0)
		var columns = make([]api.ReadColumn, 0, 0)
		for _, feature := range fgFeature.Features {
			if value, ok := (*entries)[feature.Prefix+feature.Name]; ok {
				var filter = api.Filter{Column: &feature.Name, Value: value}
				filters = append(filters, filter)
				if log.IsDebug() {
					log.Debugf("Add to filter: %s", feature.Name)
				}
			} else {
				var colName = feature.Name
				var colType = "default"
				readCol := api.ReadColumn{Column: &colName, DataReturnType: &colType}
				columns = append(columns, readCol)
				if log.IsDebug() {
					log.Debugf("Add to column: %s", feature.Name)
				}
			}
		}
		var opId = feature_store.GetFeatureGroupKeyByTDFeature(fgFeature)
		param := api.PKReadParams{DB: &testDb, Table: &testTable, Filters: &filters, ReadColumns: &columns, OperationID: &opId}
		batchReadParams = append(batchReadParams, &param)
	}
	return &batchReadParams
}

func getPkReadResponseJSON(metadata feature_store.FeatureViewMetadata) *api.BatchOpResponse {
	response := (api.BatchOpResponse)(&api.BatchResponseJSON{})
	response.Init(len(metadata.FeatureGroupFeatures))
	return &response
}

func FillPassedFeatures(features *[]interface{}, passedFeatures *map[string]*json.RawMessage, featureMetadata *map[string]*feature_store.FeatureMetadata, indexLookup *map[string]int) {
	if passedFeatures != nil {
		for featureName, passFeature := range *passedFeatures {
			var feature = (*featureMetadata)[featureName]
			var lookupKey = feature_store.GetFeatureIndexKeyByFeature(feature)
			(*features)[(*indexLookup)[lookupKey]] = passFeature
		}
	}
}
