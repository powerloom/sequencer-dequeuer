package config

import (
	"encoding/json"
	log "github.com/sirupsen/logrus"
	"os"
	"strings"
)

var SettingsObj *Settings

type Settings struct {
	ClientUrl         string   `json:"ClientUrl"`
	ContractAddress   string   `json:"ContractAddress"`
	RedisHost         string   `json:"RedisHost"`
	RedisPort         string   `json:"RedisPort"`
	ChainID           int64    `json:"ChainID"`
	FullNodes         []string `json:"FullNodes"`
	SlackReportingUrl string   `json:"SlackReportingUrl"`
}

func LoadConfig() {
	file, err := os.Open(strings.TrimSuffix(os.Getenv("CONFIG_PATH"), "/") + "/config/settings.json")
	if err != nil {
		log.Fatalf("Failed to open config file: %v", err)
	}
	defer func(file *os.File) {
		err = file.Close()
		if err != nil {
			log.Errorf("Unable to close file: %s", err.Error())
		}
	}(file)

	decoder := json.NewDecoder(file)
	config := Settings{}
	err = decoder.Decode(&config)
	if err != nil {
		log.Debugf("Failed to decode config file: %v", err)
	}

	SettingsObj = &config
}
