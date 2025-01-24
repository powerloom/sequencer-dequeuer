package config

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	log "github.com/sirupsen/logrus"
)

var SettingsObj *Settings

type DataMarketConfig struct {
	Address        string `json:"address"`
	DataSourcesUrl string `json:"dataSourcesUrl"`
	ListKey        string `json:"listKey"`
}

type Settings struct {
	ClientUrl                       string
	ContractAddress                 string
	RedisHost                       string
	RedisPort                       string
	ChainID                         int64
	FullNodes                       []string
	DataMarketAddresses             []string
	DataMarketAddressesConfig       []DataMarketConfig
	VerifySubmissionDataSourceIndex bool
	SlackReportingUrl               string
	RedisDB                         string
	DataSourcesByMarket             map[string][]string
	// number of epochs behind current epoch whose submissions are accepted
	// this is a function of the epoch size that depends on the source chain block time,
	// and the snapshot submission window that depends on Powerloom chain block time
	EpochAcceptanceWindow int
}

// Add this new method to the Settings struct
func (s *Settings) IsValidDataMarketAddress(address common.Address) bool {
	lowercaseAddress := strings.ToLower(address.Hex())
	for _, validAddress := range s.DataMarketAddresses {
		if strings.ToLower(validAddress) == lowercaseAddress {
			return true
		}
	}
	return false
}

func LoadConfig() {
	fullNodes := getEnv("FULL_NODES", "[]")
	fullNodesList := []string{}
	err := json.Unmarshal([]byte(fullNodes), &fullNodesList)
	if err != nil {
		log.Fatalf("Failed to parse FULL_NODES environment variable: %v", err)
	}

	// Parse the new config format
	dataMarketAddressesConfig := getEnv("DATA_MARKET_ADDRESSES_CONFIG", "[]")
	var dataMarketConfigList []DataMarketConfig
	err = json.Unmarshal([]byte(dataMarketAddressesConfig), &dataMarketConfigList)
	if err != nil {
		log.Fatalf("Failed to parse DATA_MARKET_ADDRESSES_CONFIG environment variable: %v", err)
	} else {
		log.Debugf("Found data sources configuration of data markets %v", dataMarketConfigList)
	}
	if len(dataMarketConfigList) == 0 {
		log.Fatalf("DATA_MARKET_ADDRESSES_CONFIG environment variable has an empty array")
	}

	// Extract addresses from config
	var dataMarketAddressesList []string
	// parse the data market addresses from the env
	dataMarketAddressesUnmarshalled := getEnv("DATA_MARKET_ADDRESSES", "")
	err = json.Unmarshal([]byte(dataMarketAddressesUnmarshalled), &dataMarketAddressesList)
	if err != nil {
		log.Fatalf("Failed to parse DATA_MARKET_ADDRESSES environment variable: %v", err)
	} else {
		log.Debugf("DATA_MARKET_ADDRESSES environment variable: %v", dataMarketAddressesList)
	}

	chainID, err := strconv.ParseInt(getEnv("CHAIN_ID", ""), 10, 64)
	if err != nil {
		log.Fatalf("Failed to parse CHAIN_ID environment variable: %v", err)
	}

	verifySubmissionDataSourceIndex, err := strconv.ParseBool(getEnv("VERIFY_SUBMISSION_DATA_SOURCE_INDEX", "false"))
	if err != nil {
		log.Fatalf("Failed to parse VERIFY_SUBMISSION_DATA_SOURCE_INDEX environment variable: %v", err)
	}

	epochAcceptanceWindow, err := strconv.Atoi(getEnv("EPOCH_ACCEPTANCE_WINDOW", "1"))
	if err != nil {
		log.Fatalf("Failed to parse EPOCH_ACCEPTANCE_WINDOW environment variable: %v", err)
	}

	// Create the config object first
	config := Settings{
		ClientUrl:                       getEnv("PROST_RPC_URL", ""),
		ContractAddress:                 getEnv("PROTOCOL_STATE_CONTRACT", ""),
		RedisHost:                       getEnv("REDIS_HOST", ""),
		RedisPort:                       getEnv("REDIS_PORT", ""),
		SlackReportingUrl:               getEnv("SLACK_REPORTING_URL", ""),
		DataMarketAddresses:             dataMarketAddressesList,
		DataMarketAddressesConfig:       dataMarketConfigList,
		RedisDB:                         getEnv("REDIS_DB", ""),
		VerifySubmissionDataSourceIndex: verifySubmissionDataSourceIndex,
		FullNodes:                       fullNodesList,
		ChainID:                         chainID,
		DataSourcesByMarket:             make(map[string][]string), // Initialize empty map
		EpochAcceptanceWindow:           epochAcceptanceWindow,
	}

	// Set the global SettingsObj
	SettingsObj = &config

	// Now fetch the data sources after SettingsObj is set
	initialSourcesByMarket, err := fetchDataSourcesList()
	if err != nil {
		log.Fatalf("Failed to fetch initial pairs: %v", err)
	}

	// Update the DataSourcesByMarket field
	SettingsObj.DataSourcesByMarket = initialSourcesByMarket
}

func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

func fetchDataSourcesList() (map[string][]string, error) {
	dataSourcesByMarket := make(map[string][]string)

	// Create a map to easily lookup config by address
	configByAddress := make(map[string]DataMarketConfig)
	for _, config := range SettingsObj.DataMarketAddressesConfig {
		configByAddress[strings.ToLower(config.Address)] = config
	}

	for _, dataMarketAddress := range SettingsObj.DataMarketAddresses {
		// Look up the config for this address
		config, exists := configByAddress[strings.ToLower(dataMarketAddress)]
		if !exists {
			log.Warnf("No configuration found for data market address: %s, skipping", dataMarketAddress)
			continue
		}

		settings, err := fetchSettingsObject(config.DataSourcesUrl)
		if err != nil {
			log.Warnf("Failed to fetch settings for %s: %v, skipping", config.Address, err)
			continue
		}

		sources, err := interfaceToStringSlice(settings[config.ListKey])
		if err != nil {
			log.Warnf("Failed to parse pairs for %s: %v, skipping", config.Address, err)
			continue
		}

		dataSourcesByMarket[strings.ToLower(dataMarketAddress)] = sources
		log.Debugf("Loaded %d sources for data market %s (type: %T, first element type: %T)",
			len(sources), dataMarketAddress, sources, sources[0])
	}

	return dataSourcesByMarket, nil
}

func fetchSettingsObject(url string) (map[string]interface{}, error) {
	// Create a custom HTTP client with insecure TLS config
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	// Use the custom client to make the request
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch settings: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("error fetching settings: %s", resp.Status)
	}

	var settings map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&settings); err != nil {
		return nil, fmt.Errorf("failed to decode settings: %v", err)
	}

	return settings, nil
}

func interfaceToStringSlice(i interface{}) ([]string, error) {
	value := reflect.ValueOf(i)

	if value.Kind() != reflect.Slice {
		return nil, fmt.Errorf("not a slice: %T", i)
	}

	var result []string
	for idx := 0; idx < value.Len(); idx++ {
		element := value.Index(idx).Interface()
		str, ok := element.(string)
		if !ok {
			return nil, fmt.Errorf("element at index %d is not a string: %T", idx, element)
		}
		result = append(result, str)
	}

	return result, nil
}
