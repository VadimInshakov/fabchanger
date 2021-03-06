package fabchanger

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/OCRVblockchain/fabchanger/config"
	"github.com/OCRVblockchain/fabchanger/configtxgen/encoder"
	"github.com/OCRVblockchain/fabchanger/configtxgen/genesisconfig"
	"github.com/OCRVblockchain/fabchanger/configtxlator/update"
	"github.com/golang/protobuf/proto"
	"github.com/golang/protobuf/ptypes/timestamp"
	"github.com/hyperledger/fabric-protos-go/common"
	cb "github.com/hyperledger/fabric-protos-go/common"
	"github.com/hyperledger/fabric-protos-go/msp"
	"github.com/hyperledger/fabric-sdk-go/pkg/client/ledger"
	"github.com/hyperledger/fabric-sdk-go/pkg/common/providers/core"
	fabricconfig "github.com/hyperledger/fabric-sdk-go/pkg/core/config"
	"github.com/hyperledger/fabric-sdk-go/pkg/fabsdk"
	"github.com/hyperledger/fabric/common/configtx"
	"github.com/hyperledger/fabric/common/tools/protolator"
	"github.com/hyperledger/fabric/common/tools/protolator/protoext/ordererext"
	"github.com/hyperledger/fabric/common/util"
	"github.com/hyperledger/fabric/core/comm"
	"github.com/hyperledger/fabric/protoutil"
	"github.com/otiai10/copy"
	"github.com/pkg/errors"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"reflect"
	"strings"
	"time"
)

var defaultConnTimeout = 3 * time.Second

type FabChanger struct {
	Config *config.Config
}

type CommonClient struct {
	*comm.GRPCClient
	Address string
	sn      string
}

func New() (*FabChanger, error) {
	configuration, err := config.GetConfig()
	if err != nil {
		return nil, err
	}

	return &FabChanger{Config: configuration}, nil
}

func (f *FabChanger) GenerateConfigs() error {

	if f.Config.Join == "orderer" {

		configtx, err := ioutil.ReadFile("./config/configtx.yaml.orderer.template")
		if err != nil {
			return err
		}

		configtxNew := strings.Replace(string(configtx), "ORDERERSTUB", f.Config.Orderer, -1)
		configtxNew = strings.Replace(configtxNew, "DOMAIN", f.Config.Connect.Domain, -1)
		if err = ioutil.WriteFile("./config/configtx.yaml", []byte(configtxNew), 0755); err != nil {
			return err
		}

		cryptoconfig, err := ioutil.ReadFile("./config/crypto-config.yaml.orderer.template")
		if err != nil {
			return err
		}

		cryptoconfigNew := strings.Replace(string(cryptoconfig), "ORDERER", f.Config.Orderer, -1)
		cryptoconfigNew = strings.Replace(cryptoconfigNew, "DOMAIN", f.Config.Connect.Domain, -1)
		if err = ioutil.WriteFile("./config/crypto-config.yaml", []byte(cryptoconfigNew), 0755); err != nil {
			return err
		}

	} else if f.Config.Join == "org" {

		configtx, err := ioutil.ReadFile("./config/configtx.yaml.org.template")
		if err != nil {
			return err
		}

		configtxNew := strings.Replace(string(configtx), "ORGMSP", f.Config.OrgToJoinMSP, -1)
		configtxNew = strings.Replace(configtxNew, "ORG", f.Config.Connect.Org, -1)
		configtxNew = strings.Replace(configtxNew, "DOMAIN", f.Config.Connect.Domain, -1)
		if err = ioutil.WriteFile("./config/configtx.yaml", []byte(configtxNew), 0755); err != nil {
			return err
		}

		cryptoconfig, err := ioutil.ReadFile("./config/crypto-config.yaml.organization.template")
		if err != nil {
			return err
		}

		cryptoconfigNew := strings.Replace(string(cryptoconfig), "ORGMSP", f.Config.OrgToJoinMSP, -1)
		cryptoconfigNew = strings.Replace(cryptoconfigNew, "ORG", f.Config.Connect.Org, -1)
		cryptoconfigNew = strings.Replace(cryptoconfigNew, "DOMAIN", f.Config.Connect.Domain, -1)
		if err = ioutil.WriteFile("./config/crypto-config.yaml", []byte(cryptoconfigNew), 0755); err != nil {
			return err
		}
	}

	// run cryptogen
	cmd := exec.Command("cryptogen", "generate", "--config=./config/crypto-config.yaml")
	err := cmd.Run()
	if err != nil {
		return errors.WithMessage(err, "cryptogen error")
	}

	if f.Config.Join == "org" {
		err = copy.Copy("./crypto-config/peerOrganizations", path.Join(f.Config.Cryptopath, "peerOrganizations"))
		if err != nil {
			return err
		}
	} else if f.Config.Join == "orderer" {
		err = copy.Copy(path.Join("./crypto-config/ordererOrganizations", f.Config.Connect.Domain, "orderers"), path.Join(f.Config.Cryptopath, "ordererOrganizations", f.Config.Connect.Domain, "orderers"))
		if err != nil {
			return err
		}
	}

	err = os.RemoveAll("./crypto-config")
	if err != nil {
		return err
	}

	return nil
}

func (f *FabChanger) ConfigTxToJSON(JSONFileName string, t *genesisconfig.TopLevel) error {
	if f.Config.Join == "org" {
		for _, org := range t.Organizations {
			if org.Name == f.Config.Connect.OrgToJoinMSP {
				og, err := encoder.NewOrdererOrgGroup(org)
				if err != nil {
					return errors.Wrapf(err, "bad org definition for org %s", org.Name)
				}

				newfile, err := os.OpenFile(JSONFileName, os.O_RDWR|os.O_CREATE, 0755)
				if err != nil {
					return err
				}

				if err := protolator.DeepMarshalJSON(newfile, &ordererext.DynamicOrdererOrgGroup{ConfigGroup: og}); err != nil {
					return errors.Wrapf(err, "malformed org definition for org: %s", org.Name)
				}

				if err := newfile.Close(); err != nil {
					return err
				}
				return nil
			}
		}
	} else if f.Config.Join == "orderer" {

		og, err := encoder.NewOrdererGroup(t.Orderer)
		if err != nil {
			return errors.Wrapf(err, "bad org definition for orderer")
		}

		newfile, err := os.OpenFile(JSONFileName, os.O_RDWR|os.O_CREATE, 0755)
		if err != nil {
			return err
		}

		if err := protolator.DeepMarshalJSON(newfile, &ordererext.DynamicOrdererGroup{ConfigGroup: og}); err != nil {
			return errors.Wrapf(err, "malformed org definition for orderer")
		}

		if err := newfile.Close(); err != nil {
			return err
		}
		return nil
	}

	return errors.Errorf("organization %s not found", f.Config.Connect.OrgToJoinMSP)

}

func (f *FabChanger) FetchBlock() (*common.Block, error) {
	fabConfig := fabricconfig.FromFile(f.Config.General.ConnectionProfile)
	sdk, err := fabsdk.New(fabConfig)
	if err != nil {
		return nil, err
	}
	defer sdk.Close()

	clientChannelContext := sdk.ChannelContext(f.Config.Channel, fabsdk.WithUser(f.Config.Identity), fabsdk.WithOrg(f.Config.MyOrg))

	ledgerClient, err := ledger.New(clientChannelContext)
	if err != nil {
		return nil, err
	}

	block, err := ledgerClient.QueryConfigBlock()
	if err != nil {
		return nil, err
	}

	b, err := proto.Marshal(block)
	if err != nil {
		return nil, err
	}

	if err = ioutil.WriteFile("block.pb", b, 0644); err != nil {
		return nil, err
	}

	return block, nil
}

func (f *FabChanger) BlockToJSON(block *common.Block, newFileName string) error {

	var buffer bytes.Buffer

	err := protolator.DeepMarshalJSON(&buffer, block)
	if err != nil {
		return err
	}

	var blockJSON = make(map[string]interface{})
	err = json.Unmarshal(buffer.Bytes(), &blockJSON)
	if err != nil {
		return err
	}

	blockJSON = blockJSON["data"].(map[string]interface{})["data"].([]interface{})[0].(map[string]interface{})["payload"].(map[string]interface{})["data"].(map[string]interface{})["config"].(map[string]interface{})
	blockJSONBytes, err := json.Marshal(blockJSON)
	if err != nil {
		return err
	}

	bufferedJSON := bytes.NewBuffer(blockJSONBytes)

	file, err := os.OpenFile(newFileName, os.O_RDWR|os.O_CREATE, 0755)
	if err != nil {
		return err
	}

	_, err = bufferedJSON.WriteTo(file)

	if err := file.Close(); err != nil {
		return err
	}

	return err
}

func (f *FabChanger) Merge(oldConfig, extendConfig, newFile string) error {
	oldFileBytes, err := ioutil.ReadFile(oldConfig)
	if err != nil {
		return err
	}
	extendConfigBytes, err := ioutil.ReadFile(extendConfig)
	if err != nil {
		return err
	}

	var oldConfigJson = make(map[string]interface{})
	err = json.Unmarshal(oldFileBytes, &oldConfigJson)

	if oldConfigJson["data"] != nil {
		oldConfigJson = oldConfigJson["data"].(map[string]interface{})["data"].([]interface{})[0].(map[string]interface{})["payload"].(map[string]interface{})["data"].(map[string]interface{})["config"].(map[string]interface{})
		if err != nil {
			return errors.New(fmt.Sprintf("can't trim map, error:", err))
		}
	}
	var extendConfigJson = make(map[string]interface{})
	err = json.Unmarshal(extendConfigBytes, &extendConfigJson)
	if err != nil {
		return err
	}
	//[0]["payload"].(map[string]interface{})["data"].(map[string]interface{})["config"]
	//.(map[string]interface{}["payload"]
	newConfigJSON := oldConfigJson

	if f.Config.Join == "org" {
		newConfigJSON["channel_group"].(map[string]interface{})["groups"].(map[string]interface{})["Application"].(map[string]interface{})["groups"].(map[string]interface{})[f.Config.OrgToJoinMSP] = extendConfigJson
	} else if f.Config.Join == "orderer" {
		newConfigJSON["channel_group"].(map[string]interface{})["groups"].(map[string]interface{})["Orderer"].(map[string]interface{})["values"].(map[string]interface{})["BatchSize"] = extendConfigJson["values"].(map[string]interface{})["BatchSize"]
		newConfigJSON["channel_group"].(map[string]interface{})["groups"].(map[string]interface{})["Orderer"].(map[string]interface{})["values"].(map[string]interface{})["BatchTimeout"] = extendConfigJson["values"].(map[string]interface{})["BatchTimeout"]
		newConfigJSON["channel_group"].(map[string]interface{})["groups"].(map[string]interface{})["Orderer"].(map[string]interface{})["values"].(map[string]interface{})["ChannelRestrictions"] = extendConfigJson["values"].(map[string]interface{})["ChannelRestrictions"]
		consenters := extendConfigJson["values"].(map[string]interface{})["ConsensusType"].(map[string]interface{})["value"].(map[string]interface{})["metadata"].(map[string]interface{})["consenters"].([]interface{})

		for _, consenter := range consenters {
			newConfigJSON["channel_group"].(map[string]interface{})["groups"].(map[string]interface{})["Orderer"].(map[string]interface{})["values"].(map[string]interface{})["ConsensusType"].(map[string]interface{})["value"].(map[string]interface{})["metadata"].(map[string]interface{})["consenters"] = append(newConfigJSON["channel_group"].(map[string]interface{})["groups"].(map[string]interface{})["Orderer"].(map[string]interface{})["values"].(map[string]interface{})["ConsensusType"].(map[string]interface{})["value"].(map[string]interface{})["metadata"].(map[string]interface{})["consenters"].([]interface{}), consenter)
		}
	} else {
		return errors.New("Join mode (--join) not specified")
	}

	bytesJson, err := json.Marshal(newConfigJSON)
	if err != nil {
		return nil
	}

	err = ioutil.WriteFile(newFile, bytesJson, 0755)
	if err != nil {
		return err
	}

	return nil
}

func (f *FabChanger) JSONToProtoConfig(source, newName string) error {

	file, err := os.OpenFile(source, os.O_RDWR|os.O_CREATE, 0755)
	if err != nil {
		return err
	}

	msgType := proto.MessageType("common.Config")
	if msgType == nil {
		return errors.Errorf("message of type %s unknown", msgType)
	}
	msg := reflect.New(msgType.Elem()).Interface().(proto.Message)

	err = protolator.DeepUnmarshalJSON(file, msg)
	if err != nil {
		return err
	}

	blockBytes, err := proto.Marshal(msg)
	if err != nil {
		return err
	}

	newFile, err := os.OpenFile(newName, os.O_RDWR|os.O_CREATE, 0755)
	if err != nil {
		return err
	}

	buf := bytes.NewBuffer(blockBytes)
	_, err = buf.WriteTo(newFile)
	if err != nil {
		return err
	}

	if err := file.Close(); err != nil {
		return err
	}
	if err := newFile.Close(); err != nil {
		return err
	}

	return err
}

func (f *FabChanger) ComputeDelta(original, updated, output string) error {

	originalFile, err := os.Open(original)
	if err != nil {
		return err
	}
	updatedFile, err := os.Open(updated)
	if err != nil {
		return err
	}
	outputFile, err := os.OpenFile(output, os.O_RDWR|os.O_CREATE, 0755)
	if err != nil {
		return err
	}

	origIn, err := ioutil.ReadAll(originalFile)
	if err != nil {
		return errors.Wrapf(err, "error reading original config")
	}

	origConf := &cb.Config{}
	err = proto.Unmarshal(origIn, origConf)
	if err != nil {
		return errors.Wrapf(err, "error unmarshaling original config")
	}

	updtIn, err := ioutil.ReadAll(updatedFile)
	if err != nil {
		return errors.Wrapf(err, "error reading updated config")
	}

	updtConf := &cb.Config{}
	err = proto.Unmarshal(updtIn, updtConf)
	if err != nil {
		return errors.Wrapf(err, "error unmarshaling updated config")
	}

	cu, err := update.Compute(origConf, updtConf)
	if err != nil {
		return errors.Wrapf(err, "error computing config update")
	}

	cu.ChannelId = f.Config.Channel

	outBytes, err := proto.Marshal(cu)
	if err != nil {
		return errors.Wrapf(err, "error marshaling computed config update")
	}

	_, err = outputFile.Write(outBytes)
	if err != nil {
		return errors.Wrapf(err, "error writing config update to output")
	}

	if err := originalFile.Close(); err != nil {
		return err
	}
	if err := updatedFile.Close(); err != nil {
		return err
	}
	if err := outputFile.Close(); err != nil {
		return err
	}

	return nil
}

func (f *FabChanger) Wrap(channelTxFile, output string) error {
	fileData, err := ioutil.ReadFile(channelTxFile)
	if err != nil {
		return err
	}

	var ConfigUpdate = &common.ConfigUpdate{}
	err = proto.Unmarshal(fileData, ConfigUpdate)
	if err != nil {
		return err
	}

	var buffer bytes.Buffer

	err = protolator.DeepMarshalJSON(&buffer, ConfigUpdate)
	if err != nil {
		return err
	}

	var wrappedDelta map[string]interface{}
	err = json.Unmarshal(buffer.Bytes(), &wrappedDelta)
	if err != nil {
		return err
	}

	var envelopeWrapper = map[string]interface{}{"payload": map[string]interface{}{"header": map[string]interface{}{"channel_header": map[string]interface{}{"channel_id": f.Config.Channel, "type": 2}}}}
	envelopeWrapper["payload"].(map[string]interface{})["data"] = map[string]interface{}{"config_update": wrappedDelta}

	envelopeWrapperJSON, err := json.Marshal(envelopeWrapper)
	if err != nil {
		return err
	}

	file, err := os.OpenFile(output, os.O_RDWR|os.O_CREATE, 0755)
	if err != nil {
		return err
	}

	_, err = file.Write(envelopeWrapperJSON)
	if err != nil {
		return err
	}

	var bufferWithEnvelope = bytes.NewBuffer(envelopeWrapperJSON)

	msgType := proto.MessageType("common.Envelope")
	if msgType == nil {
		return errors.Errorf("message of type %s unknown", msgType)
	}
	msg := reflect.New(msgType.Elem()).Interface().(proto.Message)

	err = protolator.DeepUnmarshalJSON(bufferWithEnvelope, msg)
	if err != nil {
		return err
	}

	marshaledEnvelope, err := proto.Marshal(msg)
	if err != nil {
		return err
	}

	err = ioutil.WriteFile("wrappedDelta.pb", marshaledEnvelope, 0755)
	if err != nil {
		return err
	}

	if err := file.Close(); err != nil {
		return err
	}

	return nil
}

func (f *FabChanger) Sign(input, output string) error {
	fabConfig := fabricconfig.FromFile(f.Config.General.ConnectionProfile)

	sdk, err := fabsdk.New(fabConfig)
	if err != nil {
		return err
	}
	defer sdk.Close()

	// get CryptoSuite & SigningManager for signing
	ClientProvider := sdk.Context(fabsdk.WithUser(f.Config.Identity), fabsdk.WithOrg(f.Config.MyOrg))
	Client, err := ClientProvider()
	if err != nil {
		return err
	}

	SigningManager := Client.SigningManager()

	fileBytes, err := ioutil.ReadFile(input)
	if err != nil {
		return err
	}

	var Envelope = &common.Envelope{}
	err = proto.Unmarshal(fileBytes, Envelope)

	payload, err := protoutil.UnmarshalPayload(Envelope.Payload)
	if err != nil {
		return err
	}

	configUpdateEnv, err := configtx.UnmarshalConfigUpdateEnvelope(payload.Data)
	if err != nil {
		return err
	}

	nonce, err := CreateNonce()
	if err != nil {
		return err
	}

	identitySerialized, err := serializeIdentity(f.Config.General.ClientCert, f.Config.MSPId)
	sigHeader := &cb.SignatureHeader{
		Creator: identitySerialized,
		Nonce:   nonce,
	}

	configSig := &cb.ConfigSignature{
		SignatureHeader: protoutil.MarshalOrPanic(sigHeader),
	}

	configSig.Signature, err = SigningManager.Sign(util.ConcatenateBytes(configSig.SignatureHeader, configUpdateEnv.ConfigUpdate), Client.PrivateKey())
	if err != nil {
		return err
	}

	configUpdateEnv.Signatures = append(configUpdateEnv.Signatures, configSig)

	EnvelopeSigned, err := CreateSignedEnvelope(f.Config.General.ClientCert, f.Config.MSPId, Client.PrivateKey(), f.Config.Identity, cb.HeaderType_CONFIG_UPDATE, f.Config.Channel, configUpdateEnv, SigningManager, 0, 0, nil)
	if err != nil {
		return err
	}

	// check envelope
	payloadData, err := protoutil.UnmarshalPayload(EnvelopeSigned.Payload)
	if err != nil {
		return err
	}

	if payloadData.Header == nil || payloadData.Header.ChannelHeader == nil {
		return errors.New("bad header")
	}

	_, err = protoutil.UnmarshalChannelHeader(payloadData.Header.ChannelHeader)
	if err != nil {
		return err
	}

	// write to file signed and verified envelope
	EnvelopeProto, err := proto.Marshal(EnvelopeSigned)
	if err != nil {
		return err
	}

	if err := ioutil.WriteFile(output, EnvelopeProto, 0755); err != nil {
		return err
	}
	return nil
}

func (f *FabChanger) Update(input string) error {

	fabConfig := fabricconfig.FromFile(f.Config.General.ConnectionProfile)

	ConfigProvider, err := fabConfig()
	if err != nil {
		return err
	}

	// construct broadcast client
	address, override, clientConfig, err := f.GetConfig(ConfigProvider)
	if err != nil {
		return errors.WithMessage(err, "failed to get broadcast config")
	}

	gClient, err := comm.NewGRPCClient(clientConfig)
	if err != nil {
		return errors.WithMessage(err, "failed to create OrdererClient from config")
	}

	oClient := &OrdererClient{
		CommonClient: CommonClient{
			GRPCClient: gClient,
			Address:    address,
			sn:         override}}

	bc, err := oClient.Broadcast()
	if err != nil {
		return err
	}

	broadcastclient := &BroadcastGRPCClient{Client: bc}

	// unmarshal envelope
	fileBytes, err := ioutil.ReadFile(input)
	if err != nil {
		return err
	}

	var Envelope = &cb.Envelope{}
	err = proto.Unmarshal(fileBytes, Envelope)

	// broadcast
	err = broadcastclient.Send(Envelope)
	if err != nil {
		return err
	}
	broadcastclient.Close()

	return nil
}

func (f *FabChanger) GetConfig(ConfigBackends []core.ConfigBackend) (address, override string, clientConfig comm.ClientConfig, err error) {
	orderers, ok := ConfigBackends[0].Lookup("orderers")
	if !ok {
		return "", "", comm.ClientConfig{}, errors.New("Can't to get orderer config")
	}

	ordererconf := orderers.(map[string]interface{})

	address = f.Config.Broadcast.Address
	override = ordererconf[f.Config.Broadcast.Domain].(map[string]interface{})["grpcoptions"].(map[string]interface{})["ssl-target-name-override"].(string)
	clientConfig = comm.ClientConfig{}
	clientConfig.Timeout = defaultConnTimeout
	secOpts := comm.SecureOptions{
		UseTLS:            f.Config.Broadcast.TLS,
		RequireClientCert: f.Config.Broadcast.RequireClientCert,
	}
	if secOpts.UseTLS {
		caPEM, err := ioutil.ReadFile(ordererconf[f.Config.Broadcast.Domain].(map[string]interface{})["tlscacerts"].(map[string]interface{})["path"].(string))
		if err != nil {
			return "", "", comm.ClientConfig{}, err
		}
		secOpts.ServerRootCAs = [][]byte{caPEM}
	}
	if secOpts.RequireClientCert {
		keyPEM, err := ioutil.ReadFile(f.Config.Broadcast.ClientKey)
		if err != nil {
			return "", "", comm.ClientConfig{}, err
		}
		secOpts.Key = keyPEM
		certPEM, err := ioutil.ReadFile(f.Config.Broadcast.ClientCert)
		if err != nil {
			return "", "", comm.ClientConfig{}, err
		}
		secOpts.Certificate = certPEM
	}
	clientConfig.SecOpts = secOpts

	return
}

// CreateNonce generates a nonce using the common/crypto package.
func CreateNonce() ([]byte, error) {
	nonce, err := getRandomNonce()
	return nonce, errors.WithMessage(err, "error generating random nonce")
}

func getRandomNonce() ([]byte, error) {
	key := make([]byte, 24)

	_, err := rand.Read(key)
	if err != nil {
		return nil, errors.Wrap(err, "error getting random bytes")
	}
	return key, nil
}

func CreateSignedEnvelope(
	ClientCert string,
	MSPId string,
	key core.Key,
	identity string,
	txType common.HeaderType,
	channelID string,
	dataMsg proto.Message,
	signer core.SigningManager,
	msgVersion int32,
	epoch uint64,
	tlsCertHash []byte,
) (*common.Envelope, error) {
	payloadChannelHeader := MakeChannelHeader(txType, msgVersion, channelID, epoch)
	payloadChannelHeader.TlsCertHash = tlsCertHash
	var err error
	payloadSignatureHeader := &common.SignatureHeader{}

	nonce, err := CreateNonce()
	if err != nil {
		return nil, err
	}

	identitySerialized, err := serializeIdentity(ClientCert, MSPId)
	payloadSignatureHeader = &cb.SignatureHeader{
		Creator: identitySerialized,
		Nonce:   nonce,
	}

	data, err := proto.Marshal(dataMsg)
	if err != nil {
		return nil, errors.Wrap(err, "error marshaling")
	}

	paylBytes := MarshalOrPanic(
		&common.Payload{
			Header: MakePayloadHeader(payloadChannelHeader, payloadSignatureHeader),
			Data:   data,
		},
	)

	var sig []byte
	if signer != nil {
		sig, err = signer.Sign(paylBytes, key)
		if err != nil {
			return nil, err
		}
	}

	env := &common.Envelope{
		Payload:   paylBytes,
		Signature: sig,
	}

	return env, nil
}

// MakeChannelHeader creates a ChannelHeader.
func MakeChannelHeader(headerType cb.HeaderType, version int32, chainID string, epoch uint64) *cb.ChannelHeader {
	return &cb.ChannelHeader{
		Type:    int32(headerType),
		Version: version,
		Timestamp: &timestamp.Timestamp{
			Seconds: time.Now().Unix(),
			Nanos:   0,
		},
		ChannelId: chainID,
		Epoch:     epoch,
	}
}

// MarshalOrPanic serializes a protobuf message and panics if this
// operation fails
func MarshalOrPanic(pb proto.Message) []byte {
	data, err := proto.Marshal(pb)
	if err != nil {
		panic(err)
	}
	return data
}

// MakePayloadHeader creates a Payload Header.
func MakePayloadHeader(ch *cb.ChannelHeader, sh *cb.SignatureHeader) *cb.Header {
	return &cb.Header{
		ChannelHeader:   MarshalOrPanic(ch),
		SignatureHeader: MarshalOrPanic(sh),
	}
}

func serializeIdentity(clientCert string, mspID string) ([]byte, error) {
	b, err := ioutil.ReadFile(clientCert)
	if err != nil {
		return nil, err
	}

	sId := &msp.SerializedIdentity{
		Mspid:   mspID,
		IdBytes: b,
	}
	return protoutil.MarshalOrPanic(sId), nil
}
