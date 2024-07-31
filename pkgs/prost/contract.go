package prost

import (
	"context"
	"github.com/cenkalti/backoff/v4"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	log "github.com/sirupsen/logrus"
	"sequencer-dequeuer/config"
	"sequencer-dequeuer/pkgs/contract"
	"time"
)

var Instance *contract.Contract

var (
	Client *ethclient.Client
)

func ConfigureClient() {
	var err error
	Client, err = ethclient.Dial(config.SettingsObj.ClientUrl)
	if err != nil {
		log.Fatal(err)
	}
}
func ConfigureContractInstance() {
	Instance, _ = contract.NewContract(common.HexToAddress(config.SettingsObj.ContractAddress), Client)
}

func MustQuery[K any](ctx context.Context, call func() (val K, err error)) (K, error) {
	expBackOff := backoff.NewConstantBackOff(1 * time.Second)

	var val K
	operation := func() error {
		var err error
		val, err = call()
		return err
	}
	// Use the retry package to execute the operation with backoff
	err := backoff.Retry(operation, backoff.WithMaxRetries(expBackOff, 3))
	if err != nil {
		return *new(K), err
	}
	return val, err
}
