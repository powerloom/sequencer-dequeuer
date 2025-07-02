package prost

import (
	"context"
	"crypto/tls"
	"fmt"
	"math/big"
	"net/http"
	"sequencer-dequeuer/config"
	protocolStateContractABIGen "sequencer-dequeuer/pkgs/contract"
	"sequencer-dequeuer/pkgs/redis"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	rpchelper "github.com/powerloom/rpc-helper"
	log "github.com/sirupsen/logrus"
)

var (
	RPCHelper       *rpchelper.RPCHelper
	ContractBackend *rpchelper.ContractBackend
	Client          *ethclient.Client // Deprecated: kept for backward compatibility
	Instance        *protocolStateContractABIGen.Contract
)

func ConfigureClient() {
	// Initialize RPC helper with configuration from settings
	rpcConfig := config.SettingsObj.ToRPCConfig()
	RPCHelper = rpchelper.NewRPCHelper(rpcConfig)

	// Initialize the RPC helper
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := RPCHelper.Initialize(ctx); err != nil {
		log.Errorf("Failed to initialize RPC helper: %s", err)
		log.Fatal(err)
	}

	// Keep the legacy Client for backward compatibility if needed
	rpcClient, err := rpc.DialOptions(context.Background(), config.SettingsObj.ClientUrl, rpc.WithHTTPClient(&http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}))
	if err != nil {
		log.Fatal(err)
	}

	Client = ethclient.NewClient(rpcClient)

	// Create the ContractBackend that will use the RPC helper for all contract calls
	ContractBackend = RPCHelper.NewContractBackend()

	log.Info("RPC helper initialized successfully with failover support")
}

func ConfigureContractInstance() {
	// Use ContractBackend for automatic retry and failover
	Instance, _ = protocolStateContractABIGen.NewContract(common.HexToAddress(config.SettingsObj.ContractAddress), ContractBackend)
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

// FetchCurrentDay fetches the current day from the contract and caches the result in Redis
func FetchCurrentDay(dataMarketAddress string) (*big.Int, error) {
	// Check Redis cache
	cachedDay, err := redis.Get(context.Background(), redis.DataMarketCurrentDay(dataMarketAddress))
	if err != nil {
		return nil, err
	}

	// Cache hit: return the cached value
	currentDay := new(big.Int)
	currentDay.SetString(cachedDay, 10)

	// Return error if current day is 0
	if currentDay.Cmp(big.NewInt(0)) == 0 {
		return nil, fmt.Errorf("current day is 0")
	}

	return currentDay, nil
}
