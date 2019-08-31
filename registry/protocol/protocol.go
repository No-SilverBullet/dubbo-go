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

package protocol

import (
	"github.com/apache/dubbo-go/config"
	"github.com/apache/dubbo-go/config_center"
	"sync"
)

import (
	"github.com/apache/dubbo-go/common"
	"github.com/apache/dubbo-go/common/constant"
	"github.com/apache/dubbo-go/common/extension"
	"github.com/apache/dubbo-go/common/logger"
	"github.com/apache/dubbo-go/protocol"
	"github.com/apache/dubbo-go/protocol/protocolwrapper"
	"github.com/apache/dubbo-go/registry"
	directory2 "github.com/apache/dubbo-go/registry/directory"
)

var (
	regProtocol *registryProtocol
)

type registryProtocol struct {
	invokers []protocol.Invoker
	// Registry  Map<RegistryAddress, Registry>
	registries sync.Map
	//To solve the problem of RMI repeated exposure port conflicts, the services that have been exposed are no longer exposed.
	//providerurl <--> exporter
	bounds                        sync.Map
	overrideListeners             sync.Map
	serviceConfigurationListeners sync.Map
	providerConfigurationListener *providerConfigurationListener
}

func init() {
	extension.SetProtocol("registry", GetProtocol)
}

func newRegistryProtocol() *registryProtocol {
	overrideListeners := sync.Map{}
	return &registryProtocol{
		overrideListeners:             overrideListeners,
		registries:                    sync.Map{},
		bounds:                        sync.Map{},
		serviceConfigurationListeners: sync.Map{},
		providerConfigurationListener: newProviderConfigurationListener(&overrideListeners),
	}
}
func getRegistry(regUrl *common.URL) registry.Registry {
	reg, err := extension.GetRegistry(regUrl.Protocol, regUrl)
	if err != nil {
		logger.Errorf("Registry can not connect success, program is going to panic.Error message is %s", err.Error())
		panic(err.Error())
	}
	return reg
}
func (proto *registryProtocol) Refer(url common.URL) protocol.Invoker {

	var registryUrl = url
	var serviceUrl = registryUrl.SubURL
	if registryUrl.Protocol == constant.REGISTRY_PROTOCOL {
		protocol := registryUrl.GetParam(constant.REGISTRY_KEY, "")
		registryUrl.Protocol = protocol
	}

	var reg registry.Registry

	if regI, loaded := proto.registries.Load(registryUrl.Key()); !loaded {
		reg = getRegistry(&registryUrl)
		proto.registries.Store(registryUrl.Key(), reg)
	} else {
		reg = regI.(registry.Registry)
	}

	//new registry directory for store service url from registry
	directory, err := directory2.NewRegistryDirectory(&registryUrl, reg)
	if err != nil {
		logger.Errorf("consumer service %v  create registry directory  error, error message is %s, and will return nil invoker!", serviceUrl.String(), err.Error())
		return nil
	}
	err = reg.Register(*serviceUrl)
	if err != nil {
		logger.Errorf("consumer service %v register registry %v error, error message is %s", serviceUrl.String(), registryUrl.String(), err.Error())
	}
	go directory.Subscribe(serviceUrl)

	//new cluster invoker
	cluster := extension.GetCluster(serviceUrl.GetParam(constant.CLUSTER_KEY, constant.DEFAULT_CLUSTER))

	invoker := cluster.Join(directory)
	proto.invokers = append(proto.invokers, invoker)
	return invoker
}

func (proto *registryProtocol) Export(invoker protocol.Invoker) protocol.Exporter {
	registryUrl := getRegistryUrl(invoker)
	providerUrl := getProviderUrl(invoker)

	overriderUrl := getSubscribedOverrideUrl(&providerUrl)
	// Deprecated! subscribe to override rules in 2.6.x or before.
	overrideSubscribeListener := newOverrideSubscribeListener(overriderUrl, invoker, proto)
	proto.overrideListeners.Store(overriderUrl, overrideSubscribeListener)
	proto.providerConfigurationListener.OverrideUrl(&providerUrl)
	serviceConfigurationListener := newServiceConfigurationListener(overrideSubscribeListener, &providerUrl)
	proto.serviceConfigurationListeners.Store(providerUrl.ServiceKey(), serviceConfigurationListener)
	serviceConfigurationListener.OverrideUrl(&providerUrl)

	var reg registry.Registry

	if regI, loaded := proto.registries.Load(registryUrl.Key()); !loaded {
		reg = getRegistry(&registryUrl)
		proto.registries.Store(registryUrl.Key(), reg)
	} else {
		reg = regI.(registry.Registry)
	}

	err := reg.Register(providerUrl)
	if err != nil {
		logger.Errorf("provider service %v register registry %v error, error message is %s", providerUrl.Key(), registryUrl.Key(), err.Error())
		return nil
	}

	key := providerUrl.Key()
	logger.Infof("The cached exporter keys is %v !", key)
	cachedExporter, loaded := proto.bounds.Load(key)
	if loaded {
		logger.Infof("The exporter has been cached, and will return cached exporter!")
	} else {
		wrappedInvoker := newWrappedInvoker(invoker, providerUrl)
		cachedExporter = extension.GetProtocol(protocolwrapper.FILTER).Export(wrappedInvoker)
		proto.bounds.Store(key, cachedExporter)
		logger.Infof("The exporter has not been cached, and will return a new  exporter!")
	}

	reg.Subscribe(overriderUrl, overrideSubscribeListener)
	return cachedExporter.(protocol.Exporter)

}
func (proto *registryProtocol) reExport(invoker protocol.Invoker, newUrl *common.URL) {
	key := getProviderUrl(invoker).Key()
	if oldExporter, loaded := proto.bounds.Load(key); loaded {
		wrappedNewInvoker := newWrappedInvoker(invoker, *newUrl)
		//TODO:MAY not safe
		oldExporter.(protocol.Exporter).Unexport()
		proto.bounds.Delete(key)

		proto.Export(wrappedNewInvoker)
		//TODO:  unregister & unsubscribe

	}
}
func (proto *registryProtocol) overrideWithConfig(providerUrl *common.URL, listener *overrideSubscribeListener) {

}

type overrideSubscribeListener struct {
	url           *common.URL
	originInvoker protocol.Invoker
	protocol      *registryProtocol
	configurator  config_center.Configurator
}

func newOverrideSubscribeListener(overriderUrl *common.URL, invoker protocol.Invoker, proto *registryProtocol) *overrideSubscribeListener {
	return &overrideSubscribeListener{url: overriderUrl, originInvoker: invoker, protocol: proto}
}
func (nl *overrideSubscribeListener) Notify(event *registry.ServiceEvent) {
	if isMatched(&(event.Service), nl.url) {
		nl.configurator = extension.GetDefaultConfigurator(&(event.Service))
		nl.doOverrideIfNecessary()
	}
}
func (nl *overrideSubscribeListener) doOverrideIfNecessary() {
	providerUrl := getProviderUrl(nl.originInvoker)
	key := providerUrl.Key()
	if exporter, ok := nl.protocol.bounds.Load(key); ok {
		currentUrl := exporter.(protocol.Exporter).GetInvoker().GetUrl()
		// Compatible with the 2.6.x
		nl.configurator.Configure(&providerUrl)
		// provider application level  management in 2.7.x
		for _, v := range nl.protocol.providerConfigurationListener.Configurators() {
			v.Configure(&providerUrl)
		}
		// provider service level  management in 2.7.x
		if serviceListener, ok := nl.protocol.serviceConfigurationListeners.Load(providerUrl.ServiceKey()); ok {
			for _, v := range serviceListener.(*serviceConfigurationListener).Configurators() {
				v.Configure(&providerUrl)
			}
		}

		if currentUrl.String() == providerUrl.String() {
			newRegUrl := nl.originInvoker.GetUrl()
			setProviderUrl(&newRegUrl, &providerUrl)
			nl.protocol.reExport(nl.originInvoker, &newRegUrl)
		}
	}
}

func isMatched(url *common.URL, subscribedUrl *common.URL) bool {
	// Compatible with the 2.6.x
	if len(url.GetParam(constant.CATEGORY_KEY, "")) == 0 && url.Protocol == constant.OVERRIDE_PROTOCOL {
		url.AddParam(constant.CATEGORY_KEY, constant.CONFIGURATORS_CATEGORY)
	}
	if subscribedUrl.URLEqual(*url) {
		return true
	}
	return false

}
func getSubscribedOverrideUrl(providerUrl *common.URL) *common.URL {
	newUrl := providerUrl.Clone()
	newUrl.Protocol = constant.PROVIDER_PROTOCOL
	newUrl.Params.Add(constant.CATEGORY_KEY, constant.CONFIGURATORS_CATEGORY)
	newUrl.Params.Add(constant.CHECK_KEY, "false")
	return newUrl
}

func (proto *registryProtocol) Destroy() {
	for _, ivk := range proto.invokers {
		ivk.Destroy()
	}
	proto.invokers = []protocol.Invoker{}

	proto.bounds.Range(func(key, value interface{}) bool {
		exporter := value.(protocol.Exporter)
		exporter.Unexport()
		proto.bounds.Delete(key)
		return true
	})

	proto.registries.Range(func(key, value interface{}) bool {
		reg := value.(registry.Registry)
		if reg.IsAvailable() {
			reg.Destroy()
		}
		proto.registries.Delete(key)
		return true
	})
}

func getRegistryUrl(invoker protocol.Invoker) common.URL {
	//here add * for return a new url
	url := invoker.GetUrl()
	//if the protocol == registry ,set protocol the registry value in url.params
	if url.Protocol == constant.REGISTRY_PROTOCOL {
		protocol := url.GetParam(constant.REGISTRY_KEY, "")
		url.Protocol = protocol
	}
	return url
}

func getProviderUrl(invoker protocol.Invoker) common.URL {
	url := invoker.GetUrl()
	return *url.SubURL
}
func setProviderUrl(regURL *common.URL, providerURL *common.URL) {
	regURL.SubURL = providerURL
}

func GetProtocol() protocol.Protocol {
	if regProtocol != nil {
		return regProtocol
	}
	return newRegistryProtocol()
}

type wrappedInvoker struct {
	invoker protocol.Invoker
	url     common.URL
	protocol.BaseInvoker
}

func newWrappedInvoker(invoker protocol.Invoker, url common.URL) *wrappedInvoker {
	return &wrappedInvoker{
		invoker:     invoker,
		url:         url,
		BaseInvoker: *protocol.NewBaseInvoker(common.URL{}),
	}
}
func (ivk *wrappedInvoker) GetUrl() common.URL {
	return ivk.url
}
func (ivk *wrappedInvoker) getInvoker() protocol.Invoker {
	return ivk.invoker
}

type providerConfigurationListener struct {
	registry.BaseConfigurationListener
	overrideListeners *sync.Map
}

func newProviderConfigurationListener(overrideListeners *sync.Map) *providerConfigurationListener {
	listener := &providerConfigurationListener{}
	listener.overrideListeners = overrideListeners
	//TODO:error handler
	_ = listener.BaseConfigurationListener.InitWith(config.GetProviderConfig().ApplicationConfig.Name+constant.CONFIGURATORS_SUFFIX, listener, extension.GetDefaultConfiguratorFunc())
	return listener
}

func (listener *providerConfigurationListener) Process(event *config_center.ConfigChangeEvent) {
	listener.BaseConfigurationListener.Process(event)
	listener.overrideListeners.Range(func(key, value interface{}) bool {
		value.(*overrideSubscribeListener).doOverrideIfNecessary()
		return true
	})
}

type serviceConfigurationListener struct {
	registry.BaseConfigurationListener
	overrideListener *overrideSubscribeListener
	providerUrl      *common.URL
}

func newServiceConfigurationListener(overrideListener *overrideSubscribeListener, providerUrl *common.URL) *serviceConfigurationListener {
	listener := &serviceConfigurationListener{overrideListener: overrideListener, providerUrl: providerUrl}
	//TODO:error handler
	_ = listener.BaseConfigurationListener.InitWith(providerUrl.EncodedServiceKey()+constant.CONFIGURATORS_SUFFIX, listener, extension.GetDefaultConfiguratorFunc())
	return &serviceConfigurationListener{overrideListener: overrideListener, providerUrl: providerUrl}
}

func (listener *serviceConfigurationListener) Process(event *config_center.ConfigChangeEvent) {
	listener.BaseConfigurationListener.Process(event)
	listener.overrideListener.doOverrideIfNecessary()
}
