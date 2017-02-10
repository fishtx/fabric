/*
Copyright IBM Corp. 2017 All Rights Reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

                 http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package configtx

import (
	configtxorderer "github.com/hyperledger/fabric/common/configtx/handlers/orderer"
	"github.com/hyperledger/fabric/common/util"
	cb "github.com/hyperledger/fabric/protos/common"
	ab "github.com/hyperledger/fabric/protos/orderer"
	"github.com/hyperledger/fabric/protos/utils"

	"fmt"

	"github.com/golang/protobuf/proto"
	"github.com/hyperledger/fabric/msp"
)

const (
	CreationPolicyKey = "CreationPolicy"
	msgVersion        = int32(0)
	epoch             = 0

	ApplicationGroup = "Application"
	OrdererGroup     = "Orderer"
	MSPKey           = "MSP"
)

// Template can be used to faciliate creation of config transactions
type Template interface {
	// Items returns a set of ConfigEnvelopes for the given chainID
	Envelope(chainID string) (*cb.ConfigEnvelope, error)
}

type simpleTemplateNext struct {
	configGroup *cb.ConfigGroup
}

// NewSimpleTemplateNext creates a Template using the supplied ConfigGroup
func NewSimpleTemplateNext(configGroups ...*cb.ConfigGroup) Template {
	sts := make([]Template, len(configGroups))
	for i, group := range configGroups {
		sts[i] = &simpleTemplateNext{
			configGroup: group,
		}
	}
	return NewCompositeTemplate(sts...)
}

// Envelope returns a ConfigEnvelopes for the given chainID
func (st *simpleTemplateNext) Envelope(chainID string) (*cb.ConfigEnvelope, error) {
	config, err := proto.Marshal(&cb.ConfigNext{
		Header: &cb.ChannelHeader{
			ChannelId: chainID,
			Type:      int32(cb.HeaderType_CONFIGURATION_ITEM),
		},
		Channel: st.configGroup,
	})

	if err != nil {
		return nil, err
	}

	return &cb.ConfigEnvelope{
		Config: config,
	}, nil
}

type compositeTemplate struct {
	templates []Template
}

// NewSimpleTemplate creates a Template using the source Templates
func NewCompositeTemplate(templates ...Template) Template {
	return &compositeTemplate{templates: templates}
}

func copyGroup(source *cb.ConfigGroup, target *cb.ConfigGroup) error {
	for key, value := range source.Values {
		_, ok := target.Values[key]
		if ok {
			return fmt.Errorf("Duplicate key: %s", key)
		}
		target.Values[key] = value
	}

	for key, policy := range source.Policies {
		_, ok := target.Policies[key]
		if ok {
			return fmt.Errorf("Duplicate policy: %s", key)
		}
		target.Policies[key] = policy
	}

	for key, group := range source.Groups {
		_, ok := target.Groups[key]
		if !ok {
			target.Groups[key] = cb.NewConfigGroup()
		}

		err := copyGroup(group, target.Groups[key])
		if err != nil {
			return fmt.Errorf("Error copying group %s: %s", key, err)
		}
	}
	return nil
}

// Items returns a set of ConfigEnvelopes for the given chainID, and errors only on marshaling errors
func (ct *compositeTemplate) Envelope(chainID string) (*cb.ConfigEnvelope, error) {
	channel := cb.NewConfigGroup()

	for i := range ct.templates {
		configEnv, err := ct.templates[i].Envelope(chainID)
		if err != nil {
			return nil, err
		}
		config, err := UnmarshalConfigNext(configEnv.Config)
		if err != nil {
			return nil, err
		}
		err = copyGroup(config.Channel, channel)
		if err != nil {
			return nil, err
		}
	}

	marshaledConfig, err := proto.Marshal(&cb.ConfigNext{
		Header: &cb.ChannelHeader{
			ChannelId: chainID,
			Type:      int32(cb.HeaderType_CONFIGURATION_ITEM),
		},
		Channel: channel,
	})
	if err != nil {
		return nil, err
	}

	return &cb.ConfigEnvelope{Config: marshaledConfig}, nil
}

// NewChainCreationTemplate takes a CreationPolicy and a Template to produce a Template which outputs an appropriately
// constructed list of ConfigEnvelope.  Note, using this Template in
// a CompositeTemplate will invalidate the CreationPolicy
func NewChainCreationTemplate(creationPolicy string, template Template) Template {
	result := cb.NewConfigGroup()
	result.Groups[configtxorderer.GroupKey] = cb.NewConfigGroup()
	result.Groups[configtxorderer.GroupKey].Values[CreationPolicyKey] = &cb.ConfigValue{
		Value: utils.MarshalOrPanic(&ab.CreationPolicy{
			Policy: creationPolicy,
		}),
	}

	return NewCompositeTemplate(NewSimpleTemplateNext(result), template)
}

// MakeChainCreationTransaction is a handy utility function for creating new chain transactions using the underlying Template framework
func MakeChainCreationTransaction(creationPolicy string, chainID string, signer msp.SigningIdentity, templates ...Template) (*cb.Envelope, error) {
	sSigner, err := signer.Serialize()
	if err != nil {
		return nil, fmt.Errorf("Serialization of identity failed, err %s", err)
	}

	newChainTemplate := NewChainCreationTemplate(creationPolicy, NewCompositeTemplate(templates...))
	newConfigEnv, err := newChainTemplate.Envelope(chainID)
	if err != nil {
		return nil, err
	}

	newConfigEnv.Signatures = []*cb.ConfigSignature{&cb.ConfigSignature{
		SignatureHeader: utils.MarshalOrPanic(utils.MakeSignatureHeader(sSigner, utils.CreateNonceOrPanic())),
	}}
	newConfigEnv.Signatures[0].Signature, err = signer.Sign(util.ConcatenateBytes(newConfigEnv.Signatures[0].SignatureHeader, newConfigEnv.Config))
	if err != nil {
		return nil, err
	}

	payloadChannelHeader := utils.MakeChannelHeader(cb.HeaderType_CONFIGURATION_TRANSACTION, msgVersion, chainID, epoch)
	payloadSignatureHeader := utils.MakeSignatureHeader(sSigner, utils.CreateNonceOrPanic())
	payloadHeader := utils.MakePayloadHeader(payloadChannelHeader, payloadSignatureHeader)
	payload := &cb.Payload{Header: payloadHeader, Data: utils.MarshalOrPanic(newConfigEnv)}
	paylBytes := utils.MarshalOrPanic(payload)

	// sign the payload
	sig, err := signer.Sign(paylBytes)
	if err != nil {
		return nil, err
	}

	return &cb.Envelope{Payload: paylBytes, Signature: sig}, nil
}
