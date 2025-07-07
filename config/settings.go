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
	rpchelper "github.com/powerloom/go-rpc-helper"
	"github.com/powerloom/go-rpc-helper/reporting"
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

	// RPC Configuration
	RPCNodes        []string `json:"rpc_nodes"`
	ArchiveNodes    []string `json:"archive_nodes"`
	MaxRetries      int      `json:"max_retries"`
	RetryDelayMs    int      `json:"retry_delay_ms"`
	MaxRetryDelayS  int      `json:"max_retry_delay_s"`
	RequestTimeoutS int      `json:"request_timeout_s"`
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

// ToRPCConfig creates an RPC config from the settings
func (s *Settings) ToRPCConfig() *rpchelper.RPCConfig {
	config := &rpchelper.RPCConfig{
		Nodes: func() []rpchelper.NodeConfig {
			var nodes []rpchelper.NodeConfig
			for _, url := range s.RPCNodes {
				nodes = append(nodes, rpchelper.NodeConfig{URL: url})
			}
			return nodes
		}(),
		ArchiveNodes: func() []rpchelper.NodeConfig {
			var nodes []rpchelper.NodeConfig
			for _, url := range s.ArchiveNodes {
				nodes = append(nodes, rpchelper.NodeConfig{URL: url})
			}
			return nodes
		}(),
		MaxRetries:     s.MaxRetries,
		RetryDelay:     time.Duration(s.RetryDelayMs) * time.Millisecond,
		MaxRetryDelay:  time.Duration(s.MaxRetryDelayS) * time.Second,
		RequestTimeout: time.Duration(s.RequestTimeoutS) * time.Second,
	}

	// Add webhook configuration if Slack reporting URL is provided
	if s.SlackReportingUrl != "" {
		config.WebhookConfig = &reporting.WebhookConfig{
			URL:     s.SlackReportingUrl,
			Timeout: time.Duration(s.RequestTimeoutS) * time.Second,
			Retries: s.MaxRetries,
		}
	}

	return config
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

	// Parse RPC nodes from environment
	rpcNodesStr := getEnv("RPC_NODES", "[]")
	var rpcNodes []string
	if err := json.Unmarshal([]byte(rpcNodesStr), &rpcNodes); err != nil {
		log.Fatalf("Failed to parse RPC_NODES environment variable: %v", err)
	}

	// Parse archive nodes from environment
	archiveNodesStr := getEnv("ARCHIVE_NODES", "[]")
	var archiveNodes []string
	if err := json.Unmarshal([]byte(archiveNodesStr), &archiveNodes); err != nil {
		log.Fatalf("Failed to parse ARCHIVE_NODES environment variable: %v", err)
	}

	// Helper function to parse int environment variables
	getEnvInt := func(key string, defaultValue int) int {
		if val := getEnv(key, ""); val != "" {
			if intVal, err := strconv.Atoi(val); err == nil {
				return intVal
			}
		}
		return defaultValue
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
		RPCNodes:                        rpcNodes,
		ArchiveNodes:                    archiveNodes,
		MaxRetries:                      getEnvInt("MAX_RETRIES", 3),
		RetryDelayMs:                    getEnvInt("RETRY_DELAY_MS", 500),
		MaxRetryDelayS:                  getEnvInt("MAX_RETRY_DELAY_S", 30),
		RequestTimeoutS:                 getEnvInt("REQUEST_TIMEOUT_S", 30),
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
