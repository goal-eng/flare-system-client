package protocol

import (
	"crypto/ecdsa"

	"flare-tlc/client/config"
	globalConfig "flare-tlc/config"
	"flare-tlc/utils/chain"
	"flare-tlc/utils/contracts/submission"
	"flare-tlc/utils/credentials"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"
)

// Private keys and addresses needed for protocol voter
type protocolContext struct {
	submitPrivateKey       *ecdsa.PrivateKey  // sign tx for submit1, submit2, submit3
	submitSignaturesTxOpts *bind.TransactOpts // submitSignatures
	signerPrivateKey       *ecdsa.PrivateKey  // sign data for submitSignatures

	submitContractAddress common.Address
	signingAddress        common.Address // address of signerPrivateKey
	submitAddress         common.Address // address of submitPrivateKey
}

type contractSelectors struct {
	submit1          []byte
	submit2          []byte
	submit3          []byte
	submitSignatures []byte
}

func newProtocolContext(cfg *config.ClientConfig) (*protocolContext, error) {
	ctx := &protocolContext{}

	chainID := cfg.ChainConfig().ChainID
	var err error

	// Credentials
	ctx.signerPrivateKey, err = globalConfig.PrivateKeyFromConfig(cfg.Credentials.SigningPolicyPrivateKeyFile,
		cfg.Credentials.SigningPolicyPrivateKey)
	if err != nil {
		return nil, errors.Wrap(err, "error creating signer private key")
	}

	ctx.submitPrivateKey, err = globalConfig.PrivateKeyFromConfig(cfg.Credentials.ProtocolManagerSubmitPrivateKeyFile,
		cfg.Credentials.ProtocolManagerSubmitPrivateKey)
	if err != nil {
		return nil, errors.Wrap(err, "error creating submit private key")
	}

	submitSignaturesPk, err := globalConfig.PrivateKeyFromConfig(cfg.Credentials.ProtocolManagerSubmitSignaturesPrivateKeyFile,
		cfg.Credentials.ProtocolManagerSubmitSignaturesPrivateKey)
	if err != nil {
		return nil, errors.Wrap(err, "error reading submit signatures private key")
	}
	ctx.submitSignaturesTxOpts, _, err = credentials.CredentialsFromPrivateKey(submitSignaturesPk, chainID)
	if err != nil {
		return nil, errors.Wrap(err, "error creating submit signatures tx opts")
	}

	// Addresses
	ctx.signingAddress, err = chain.PrivateKeyToEthAddress(ctx.signerPrivateKey)
	if err != nil {
		return nil, errors.Wrap(err, "error getting signing address")
	}
	ctx.submitAddress, err = chain.PrivateKeyToEthAddress(ctx.submitPrivateKey)
	if err != nil {
		return nil, errors.Wrap(err, "error getting submit address")
	}
	ctx.submitContractAddress = cfg.ContractAddresses.Submission

	return ctx, nil
}

func newContractSelectors() contractSelectors {
	submissionABI, err := submission.SubmissionMetaData.GetAbi()
	if err != nil {
		// panic, this error is fatal
		panic(err)
	}
	return contractSelectors{
		submit1:          submissionABI.Methods["submit1"].ID,
		submit2:          submissionABI.Methods["submit2"].ID,
		submit3:          submissionABI.Methods["submit3"].ID,
		submitSignatures: submissionABI.Methods["submitSignatures"].ID,
	}
}
