package utils

import (
	"fmt"
	"math/big"
	"sequencer-dequeuer/config"
	"sequencer-dequeuer/pkgs"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	types "github.com/ethereum/go-ethereum/signer/core/apitypes"
)

// EIP712Domain represents the EIP-712 domain details
type EIP712Domain struct {
	Name              string
	Version           string
	ChainID           *big.Int
	VerifyingContract common.Address
}

func HashRequest(request *pkgs.Request) []byte {
	signerData := &types.TypedData{
		PrimaryType: "EIPRequest",
		Types: types.Types{
			"EIPRequest": []types.Type{
				{Name: "slotId", Type: "uint256"},
				{Name: "deadline", Type: "uint256"},
				{Name: "snapshotCid", Type: "string"},
				{Name: "epochId", Type: "uint256"},
				{Name: "projectId", Type: "string"},
			},
			"EIP712Domain": []types.Type{
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"},
				{Name: "verifyingContract", Type: "address"},
			},
		},
		Domain: types.TypedDataDomain{
			Name:              "PowerloomProtocolContract",
			Version:           "0.1",
			ChainId:           (*math.HexOrDecimal256)(math.MustParseBig256(strconv.FormatInt(config.SettingsObj.ChainID, 10))),
			VerifyingContract: config.SettingsObj.ContractAddress,
		},
		Message: types.TypedDataMessage{
			"slotId":      (*math.HexOrDecimal256)(big.NewInt(int64(request.SlotId))),
			"deadline":    (*math.HexOrDecimal256)(big.NewInt(int64(request.Deadline))),
			"snapshotCid": request.SnapshotCid,
			"epochId":     (*math.HexOrDecimal256)(big.NewInt(int64(request.EpochId))),
			"projectId":   request.ProjectId,
		},
	}

	typedDataHash, _ := signerData.HashStruct(signerData.PrimaryType, signerData.Message)
	domainSeparator, _ := signerData.HashStruct("EIP712Domain", signerData.Domain.Map())
	domainSeparatorBytes := []byte(domainSeparator)
	typedDataHashBytes := []byte(typedDataHash)
	rawData := append([]byte{0x19, 0x01}, domainSeparatorBytes...)
	rawData = append(rawData, typedDataHashBytes...)
	hash := crypto.Keccak256Hash(rawData)
	return hash.Bytes()
}

func RecoverAddress(msgHash, signature []byte) (common.Address, error) {
	if len(signature) != 65 {
		return common.Address{}, fmt.Errorf("invalid signature length: %d", len(signature))
	}

	if signature[64] != 27 && signature[64] != 28 {
		return common.Address{}, fmt.Errorf("invalid recovery id: %d", signature[64])
	}
	signature[64] -= 27

	pubKeyRaw, _ := crypto.Ecrecover(msgHash, signature)

	pubKey, err := crypto.UnmarshalPubkey(pubKeyRaw)
	if err != nil {
		return common.Address{}, err
	}

	recoveredAddr := crypto.PubkeyToAddress(*pubKey)
	return recoveredAddr, nil
}
