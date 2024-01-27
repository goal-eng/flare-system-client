package protocol

import (
	"bytes"
	"flare-tlc/client/config"
	"flare-tlc/client/shared"
	"flare-tlc/logger"
	"flare-tlc/utils"
	"flare-tlc/utils/chain"
	"fmt"
	"time"

	mapset "github.com/deckarep/golang-set/v2"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/pkg/errors"
)

const (
	submitterGetDataTimeout       = 5 * time.Second
	signatureSubmitterDataTimeout = 1 * time.Second
)

type SubmitterBase struct {
	ethClient *ethclient.Client

	protocolContext *protocolContext

	epoch        *utils.Epoch
	selector     []byte
	subProtocols []*SubProtocol

	startOffset   time.Duration
	submitRetries int    // number of retries for submitting tx
	name          string // e.g., "submit1", "submit2", "submit3", "signatureSubmitter"
}

type Submitter struct {
	SubmitterBase

	epochOffset int64 // offset from current epoch, e.g., -1, 0
}

type SignatureSubmitter struct {
	SubmitterBase

	maxRounds        int // number of rounds for sending submitSignatures tx
	dataFetchRetries int // number of retries for fetching data of each provider
}

func (s *SubmitterBase) submit(payload []byte) bool {
	sendResult := <-shared.ExecuteWithRetry(func() (any, error) {
		err := chain.SendRawTx(s.ethClient, s.protocolContext.submitPrivateKey, s.protocolContext.submitContractAddress, payload)
		if err != nil {
			return nil, errors.Wrap(err, fmt.Sprintf("error sending submit tx for submitter %s tx", s.name))
		}
		return nil, nil
	}, s.submitRetries, shared.TxRetryInterval)
	if sendResult.Success {
		logger.Info("submitter %s submitted tx", s.name)
	}
	return sendResult.Success
}

func (s *SubmitterBase) GetEpochTicker() *utils.EpochTicker {
	return utils.NewEpochTicker(s.startOffset, s.epoch)
}

func newSubmitter(
	ethClient *ethclient.Client,
	pc *protocolContext,
	epoch *utils.Epoch,
	submitCfg *config.SubmitConfig,
	selector []byte,
	subProtocols []*SubProtocol,
	epochOffset int64,
	name string,
) *Submitter {
	return &Submitter{
		SubmitterBase: SubmitterBase{
			ethClient:       ethClient,
			protocolContext: pc,
			epoch:           epoch,
			selector:        selector,
			subProtocols:    subProtocols,
			startOffset:     submitCfg.StartOffset,
			submitRetries:   max(1, submitCfg.TxSubmitRetries),
			name:            name,
		},
		epochOffset: epochOffset,
	}
}

func (s *Submitter) GetPayload(currentEpoch int64) ([]byte, error) {
	channels := make([]<-chan shared.ExecuteStatus[*SubProtocolResponse], len(s.subProtocols))
	for i, protocol := range s.subProtocols {
		channels[i] = protocol.getDataWithRetry(
			currentEpoch+s.epochOffset,
			s.name,
			s.protocolContext.submitAddress.Hex(),
			1,
			submitterGetDataTimeout,
			IdentityDataVerifier,
		)
	}

	buffer := bytes.NewBuffer(nil)
	buffer.Write(s.selector)
	for _, channel := range channels {
		data := <-channel
		if !data.Success || data.Value.Status != "OK" {
			logger.Error("error getting data for submitter %s: %s", s.name, data.Message)
			continue
		}
		buffer.Write(data.Value.Data)
	}
	return buffer.Bytes(), nil
}

func (s *Submitter) RunEpoch(currentEpoch int64) {
	logger.Debug("submitter %s running epoch %d", s.name, currentEpoch)
	logger.Debug("  epoch is [%v, %v], now is %v", s.epoch.StartTime(currentEpoch), s.epoch.EndTime(currentEpoch), time.Now())

	payload, err := s.GetPayload(currentEpoch)
	if err == nil {
		s.submit(payload)
	}
}

func newSignatureSubmitter(
	ethClient *ethclient.Client,
	pc *protocolContext,
	epoch *utils.Epoch,
	submitCfg *config.SubmitSignaturesConfig,
	selector []byte,
	subProtocols []*SubProtocol,
) *SignatureSubmitter {
	return &SignatureSubmitter{
		SubmitterBase: SubmitterBase{
			ethClient:       ethClient,
			protocolContext: pc,
			epoch:           epoch,
			startOffset:     submitCfg.StartOffset,
			selector:        selector,
			subProtocols:    subProtocols,
			submitRetries:   max(1, submitCfg.TxSubmitRetries),
			name:            "submitSignatures",
		},
		maxRounds:        submitCfg.MaxRounds,
		dataFetchRetries: submitCfg.DataFetchRetries,
	}
}

// Payload data should be valid (data length 38, additional data length <= maxuint16 - 104)
func (s *SignatureSubmitter) WritePayload(buffer *bytes.Buffer, currentEpoch int64, data *SubProtocolResponse) error {
	dataHash := accounts.TextHash(crypto.Keccak256(data.Data))
	signature, err := crypto.Sign(dataHash, s.protocolContext.signerPrivateKey)
	if err != nil {
		return errors.Wrap(err, "error signing submitSignatures data")
	}

	epochBytes := shared.Uint32toBytes(uint32(currentEpoch - 1))
	lengthBytes := shared.Uint16toBytes(uint16(104 + len(data.AdditionalData)))

	buffer.WriteByte(100)        // Protocol ID (1 byte)
	buffer.Write(epochBytes[:])  // Epoch (4 bytes)
	buffer.Write(lengthBytes[:]) // Length (2 bytes)

	buffer.WriteByte(0)     // Type (1 byte)
	buffer.Write(data.Data) // Message (38 bytes)

	buffer.WriteByte(signature[64] + 27) // V (1 byte)
	buffer.Write(signature[0:32])        // R (32 bytes)
	buffer.Write(signature[32:64])       // S (32 bytes)

	buffer.Write(data.AdditionalData)
	return nil
}

// 1. Run every sub-protocol provider with delay of 1 second at most five times
// 2. repeat 1 for each sub-protocol provider not giving valid answer
// Repeat 1 and 2 until all sub-protocol providers give valid answer or we did 10 rounds
func (s *SignatureSubmitter) RunEpoch(currentEpoch int64) {
	logger.Debug("signatureSubmitter %s running epoch %d", s.name, currentEpoch)
	logger.Debug("  epoch is [%v, %v], now is %v", s.epoch.StartTime(currentEpoch), s.epoch.EndTime(currentEpoch), time.Now())

	protocolsToSend := mapset.NewSet[int]()
	for i := range s.subProtocols {
		protocolsToSend.Add(i)
	}
	channels := make([]<-chan shared.ExecuteStatus[*SubProtocolResponse], len(s.subProtocols))
	for i := 0; i < s.maxRounds && protocolsToSend.Cardinality() > 0; i++ {
		for i, protocol := range s.subProtocols {
			if !protocolsToSend.Contains(i) {
				continue
			}
			channels[i] = protocol.getDataWithRetry(
				currentEpoch-1,
				"submitSignatures",
				s.protocolContext.submitSignaturesTxOpts.From.Hex(),
				s.dataFetchRetries,
				signatureSubmitterDataTimeout,
				SignatureSubmitterDataVerifier,
			)
		}

		protocolsToSendCopy := protocolsToSend.Clone() // copy in case of submit failure

		buffer := bytes.NewBuffer(nil)
		buffer.Write(s.selector)
		for i := range s.subProtocols {
			if !protocolsToSend.Contains(i) {
				continue
			}

			data := <-channels[i]
			if !data.Success {
				logger.Error("error getting data for submitter %s: %s", s.name, data.Message)
				continue
			}
			err := s.WritePayload(buffer, currentEpoch-1, data.Value)
			if err != nil {
				logger.Error("error writing payload for submitter %s: %v", s.name, err)
				continue
			}
			protocolsToSend.Remove(i)
		}
		if protocolsToSendCopy.Cardinality() > protocolsToSend.Cardinality() {
			if !s.submit(buffer.Bytes()) {
				protocolsToSend = protocolsToSendCopy
			}
		} else {
			logger.Info("signatureSubmitter %s did not get any new data", s.name)
		}
	}
}
