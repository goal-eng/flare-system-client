package registration

import (
	"context"
	flarectx "flare-tlc/client/context"
	"flare-tlc/config"
	"flare-tlc/database"
	"flare-tlc/logger"
	"flare-tlc/utils/chain"
	"flare-tlc/utils/contracts/system"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

// Start tic voter registration & signing policy voter client 2 hours
// before end of epoch (reward epoch 3.5 days)
//  1. Listen until VotePowerBlockSelected (enabled voter registration) event is emitted
//  2. Call RegisterVoter function on VoterRegistry
//  3. Wait until we get voter registered event
//  4. Wait until SigningPolicyInitialized is emitted
//  5. Call signNewSigningPolicy
//  6. Wait until SigningPolicySigned is emitted (for the voter)

type registrationClient struct {
	db registrationClientDB

	systemsManagerClient systemsManagerContractClient
	relayClient          relayContractClient
	registryClient       registryContractClient

	identityAddress common.Address
}

type registrationClientDB interface {
	FetchLogsByAddressAndTopic0(common.Address, string, int64, int64) ([]database.Log, error)
}

type registrationClientDBGorm struct {
	db *gorm.DB
}

func (g registrationClientDBGorm) FetchLogsByAddressAndTopic0(
	address common.Address, topic0 string, fromBlock int64, toBlock int64,
) ([]database.Log, error) {
	return database.FetchLogsByAddressAndTopic0(g.db, address.Hex(), topic0, fromBlock, toBlock)
}

func NewRegistrationClient(ctx flarectx.ClientContext) (*registrationClient, error) {
	cfg := ctx.Config()
	if !cfg.Clients.EnabledRegistration {
		return nil, nil
	}

	chainCfg := cfg.ChainConfig()
	ethClient, err := chainCfg.DialETH()
	if err != nil {
		return nil, err
	}

	senderPk, err := config.ReadFileToString(cfg.Credentials.SystemClientSenderPrivateKeyFile)
	if err != nil {
		return nil, errors.Wrap(err, "error reading sender private key")
	}
	senderTxOpts, _, err := chain.CredentialsFromPrivateKey(senderPk, chainCfg.ChainID)
	if err != nil {
		return nil, errors.Wrap(err, "error creating sender register tx opts")
	}

	signerPkString, err := config.ReadFileToString(cfg.Credentials.SigningPolicyPrivateKeyFile)
	if err != nil {
		return nil, errors.Wrap(err, "error reading signer private key")
	}
	signerPk, err := chain.PrivateKeyFromHex(signerPkString)
	if err != nil {
		return nil, errors.Wrap(err, "error creating signer private key")
	}

	systemsManagerClient, err := NewSystemsManagerClient(
		ethClient,
		cfg.ContractAddresses.SystemsManager,
		senderTxOpts,
		signerPk,
	)
	if err != nil {
		return nil, err
	}

	relayClient, err := NewRelayContractClient(
		ethClient,
		cfg.ContractAddresses.Relay,
	)
	if err != nil {
		return nil, err
	}

	registryClient, err := NewRegistryContractClient(
		ethClient,
		cfg.ContractAddresses.VoterRegistry,
		senderTxOpts,
		signerPk,
	)
	if err != nil {
		return nil, err
	}

	identityAddress := cfg.Identity.Address
	if identityAddress == chain.EmptyAddress {
		return nil, errors.New("no identity address provided")
	}
	logger.Debug("Identity addr %v", identityAddress)

	db := registrationClientDBGorm{db: ctx.DB()}
	return &registrationClient{
		db:                   db,
		systemsManagerClient: systemsManagerClient,
		relayClient:          relayClient,
		registryClient:       registryClient,
		identityAddress:      identityAddress,
	}, nil
}

// Run runs the registration client, should be called in a goroutine
func (c *registrationClient) Run() error {
	return c.RunContext(context.Background())
}

func (c *registrationClient) RunContext(ctx context.Context) error {
	epoch, err := c.systemsManagerClient.RewardEpochFromChain()
	if err != nil {
		return err
	}
	vpbsListener := c.systemsManagerClient.VotePowerBlockSelectedListener(c.db, epoch)

	for {
		// Wait until VotePowerBlockSelected (enabled voter registration) event is emitted
		logger.Debug("Waiting for VotePowerBlockSelected event")

		var powerBlockData *system.FlareSystemsManagerVotePowerBlockSelected

		select {
		case powerBlockData = <-vpbsListener:
			logger.Info("VotePowerBlockSelected event emitted for epoch %v", powerBlockData.RewardEpochId)

		case <-ctx.Done():
			return ctx.Err()
		}

		if !c.verifyEpoch(powerBlockData.RewardEpochId) {
			logger.Info("Skipping registration process for epoch %v", powerBlockData.RewardEpochId)
			continue
		}

		// Call RegisterVoter function on VoterRegistry
		registerResult := <-c.registryClient.RegisterVoter(powerBlockData.RewardEpochId, c.identityAddress)
		if !registerResult.Success {
			logger.Error("RegisterVoter failed %s", registerResult.Message)
			continue
		}

		logger.Info("RegisterVoter success")

		// Wait until we get voter registered event
		// Already in RegisterVoter

		// Wait until SigningPolicyInitialized event is emitted
		signingPolicy := <-c.relayClient.SigningPolicyInitializedListener(c.db, powerBlockData.Timestamp)
		logger.Info("SigningPolicyInitialized event emitted for epoch %v", signingPolicy.RewardEpochId)

		// Call signNewSigningPolicy
		signingResult := <-c.systemsManagerClient.SignNewSigningPolicy(signingPolicy.RewardEpochId, signingPolicy.SigningPolicyBytes)
		if !signingResult.Success {
			logger.Error("SignNewSigningPolicy failed %s", signingResult.Message)
			continue
		}
	}
}

func (c *registrationClient) verifyEpoch(epochId *big.Int) bool {
	epochIdResult := <-c.systemsManagerClient.GetCurrentRewardEpochId()
	if !epochIdResult.Success {
		logger.Error("GetCurrentRewardEpochId failed %s", epochIdResult.Message)
		return false
	}
	currentEpochId := epochIdResult.Value
	if epochId.Cmp(currentEpochId) <= 0 {
		logger.Warn("Epoch mismatch: current %v >= next %v", currentEpochId, epochId)
		return false
	}
	return true
}
