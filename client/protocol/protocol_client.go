package protocol

import (
	clientContext "flare-tlc/client/context"
	"flare-tlc/client/registration"
	"flare-tlc/utils"
	"flare-tlc/utils/contracts/system"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/pkg/errors"
)

type ProtocolClient struct {
	subProtocols []*SubProtocol
	eth          *ethclient.Client

	protocolCredentials *protocolCredentials
	protocolAddresses   *protocolAddresses

	submitter1         *Submitter
	submitter2         *Submitter
	signatureSubmitter *SignatureSubmitter

	votingEpoch   *utils.Epoch
	systemManager *system.FlareSystemManager
}

func NewProtocolClient(ctx clientContext.ClientContext) (*ProtocolClient, error) {
	cfg := ctx.Config()

	if !cfg.Voting.EnabledProtocolVoting {
		return nil, nil
	}

	chainCfg := cfg.ChainConfig()
	cl, err := chainCfg.DialETH()
	if err != nil {
		return nil, err
	}

	systemManager, err := system.NewFlareSystemManager(cfg.ContractAddresses.SystemManager, cl)
	if err != nil {
		return nil, errors.Wrap(err, "error creating system manager contract")
	}

	votingEpoch, err := registration.VotingEpochFromChain(systemManager)
	if err != nil {
		return nil, errors.Wrap(err, "error getting voting epoch")
	}

	credentials, err := newProtocolCredentials(chainCfg.ChainID, &cfg.Credentials)
	if err != nil {
		return nil, err
	}

	addresses, err := newProtocolAddresses(credentials, &cfg.ContractAddresses)
	if err != nil {
		return nil, err
	}

	var subProtocols []*SubProtocol
	for _, protocol := range cfg.Protocol {
		subProtocols = append(subProtocols, NewSubProtocol(protocol))
	}

	pc := &ProtocolClient{
		eth:                 cl,
		protocolCredentials: credentials,
		protocolAddresses:   addresses,
		subProtocols:        subProtocols,
		votingEpoch:         votingEpoch,
		systemManager:       systemManager,
	}

	selectors := newContractSelectors()

	pc.submitter1 = newSubmitter(cl, credentials, addresses, votingEpoch,
		&cfg.Submit1, selectors.submit1, subProtocols, 0, "submit1")
	pc.submitter2 = newSubmitter(cl, credentials, addresses, votingEpoch,
		&cfg.Submit2, selectors.submit2, subProtocols, -1, "submit2")
	pc.signatureSubmitter = newSignatureSubmitter(cl, credentials, addresses, votingEpoch,
		&cfg.SignatureSubmitter, selectors.submitSignatures, subProtocols)

	return pc, nil
}

func (c *ProtocolClient) Run() error {
	go c.submitter1.Run()
	go c.submitter2.Run()
	go c.signatureSubmitter.Run()

	return nil
}
