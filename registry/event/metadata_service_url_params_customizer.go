/*
 * Licensed to the Apache Software Foundation (ASF) under one or more
 * contributor license agreements.  See the NOTICE file distributed with
 * this work for additional information regarding copyright ownership.
 * The ASF licenses this file to You under the Apache License, Version 2.0
 * (the "License"); you may not use this file except in compliance with
 * the License.  You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package event

import (
	"encoding/json"
)

import (
	"github.com/apache/dubbo-go/common"
	"github.com/apache/dubbo-go/common/constant"
	"github.com/apache/dubbo-go/common/extension"
	"github.com/apache/dubbo-go/common/logger"
	"github.com/apache/dubbo-go/registry"
)

func init() {
	extension.AddCustomizers(&metadataServiceURLParamsMetadataCustomizer{})
}

type metadataServiceURLParamsMetadataCustomizer struct {
}

// GetPriority will return 0 so that it will be invoked in front of user defining Customizer
func (m *metadataServiceURLParamsMetadataCustomizer) GetPriority() int {
	return 0
}

func (m *metadataServiceURLParamsMetadataCustomizer) Customize(instance registry.ServiceInstance) {
	ms, err := getMetadataService()
	if err != nil {
		logger.Errorf("could not find the metadata service", err)
		return
	}
	url := ms.GetMetadataServiceURL()
	ps := m.convertToParams(url)
	str, err := json.Marshal(ps)
	if err != nil {
		logger.Errorf("could not transfer the map to json", err)
		return
	}
	instance.GetMetadata()[constant.METADATA_SERVICE_URL_PARAMS_PROPERTY_NAME] = string(str)
}

func (m *metadataServiceURLParamsMetadataCustomizer) convertToParams(url *common.URL) map[string]string {
	// usually there will be only one protocol
	res := make(map[string]string, 1)
	// those keys are useless
	p := make(map[string]string, len(url.GetParams()))
	for k, v := range url.GetParams() {
		// we will ignore that
		if !common.IncludeKeys.Contains(k) || len(v) == 0 || len(v[0]) == 0 {
			continue
		}
		p[k] = v[0]
	}
	p[constant.PORT_KEY] = url.Port
	p[constant.PROTOCOL_KEY] = url.Protocol
	return res
}
