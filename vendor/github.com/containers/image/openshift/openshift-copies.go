package openshift

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"time"

	"github.com/ghodss/yaml"
	"github.com/imdario/mergo"
	utilerrors "k8s.io/kubernetes/pkg/util/errors"
	"k8s.io/kubernetes/pkg/util/homedir"
	utilnet "k8s.io/kubernetes/pkg/util/net"
)

// restTLSClientConfig is a modified copy of k8s.io/kubernets/pkg/client/restclient.TLSClientConfig.
// restTLSClientConfig contains settings to enable transport layer security
type restTLSClientConfig struct {
	// Server requires TLS client certificate authentication
	CertFile string
	// Server requires TLS client certificate authentication
	KeyFile string
	// Trusted root certificates for server
	CAFile string

	// CertData holds PEM-encoded bytes (typically read from a client certificate file).
	// CertData takes precedence over CertFile
	CertData []byte
	// KeyData holds PEM-encoded bytes (typically read from a client certificate key file).
	// KeyData takes precedence over KeyFile
	KeyData []byte
	// CAData holds PEM-encoded bytes (typically read from a root certificates bundle).
	// CAData takes precedence over CAFile
	CAData []byte
}

// restConfig is a modified copy of k8s.io/kubernets/pkg/client/restclient.Config.
// Config holds the common attributes that can be passed to a Kubernetes client on
// initialization.
type restConfig struct {
	// Host must be a host string, a host:port pair, or a URL to the base of the apiserver.
	// If a URL is given then the (optional) Path of that URL represents a prefix that must
	// be appended to all request URIs used to access the apiserver. This allows a frontend
	// proxy to easily relocate all of the apiserver endpoints.
	Host string

	// Server requires Basic authentication
	Username string
	Password string

	// Server requires Bearer authentication. This client will not attempt to use
	// refresh tokens for an OAuth2 flow.
	// TODO: demonstrate an OAuth2 compatible client.
	BearerToken string

	// TLSClientConfig contains settings to enable transport layer security
	restTLSClientConfig

	// Server should be accessed without verifying the TLS
	// certificate. For testing only.
	Insecure bool
}

// ClientConfig is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.ClientConfig.
// ClientConfig is used to make it easy to get an api server client
type clientConfig interface {
	// ClientConfig returns a complete client config
	ClientConfig() (*restConfig, error)
}

// defaultClientConfig is a modified copy of openshift/origin/pkg/cmd/util/clientcmd.DefaultClientConfig.
func defaultClientConfig() clientConfig {
	loadingRules := newOpenShiftClientConfigLoadingRules()
	// REMOVED: Allowing command-line overriding of loadingRules
	// REMOVED: clientcmd.ConfigOverrides

	clientConfig := newNonInteractiveDeferredLoadingClientConfig(loadingRules)

	return clientConfig
}

var recommendedHomeFile = path.Join(homedir.HomeDir(), ".kube/config")

// newOpenShiftClientConfigLoadingRules is a modified copy of openshift/origin/pkg/cmd/cli/config.NewOpenShiftClientConfigLoadingRules.
// NewOpenShiftClientConfigLoadingRules returns file priority loading rules for OpenShift.
// 1. --config value
// 2. if KUBECONFIG env var has a value, use it. Otherwise, ~/.kube/config file
func newOpenShiftClientConfigLoadingRules() *clientConfigLoadingRules {
	chain := []string{}

	envVarFile := os.Getenv("KUBECONFIG")
	if len(envVarFile) != 0 {
		chain = append(chain, filepath.SplitList(envVarFile)...)
	} else {
		chain = append(chain, recommendedHomeFile)
	}

	return &clientConfigLoadingRules{
		Precedence: chain,
		// REMOVED: Migration support; run (oc login) to trigger migration
	}
}

// deferredLoadingClientConfig is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.DeferredLoadingClientConfig.
// DeferredLoadingClientConfig is a ClientConfig interface that is backed by a set of loading rules
// It is used in cases where the loading rules may change after you've instantiated them and you want to be sure that
// the most recent rules are used.  This is useful in cases where you bind flags to loading rule parameters before
// the parse happens and you want your calling code to be ignorant of how the values are being mutated to avoid
// passing extraneous information down a call stack
type deferredLoadingClientConfig struct {
	loadingRules *clientConfigLoadingRules

	clientConfig clientConfig
}

// NewNonInteractiveDeferredLoadingClientConfig is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.NewNonInteractiveDeferredLoadingClientConfig.
// NewNonInteractiveDeferredLoadingClientConfig creates a ConfigClientClientConfig using the passed context name
func newNonInteractiveDeferredLoadingClientConfig(loadingRules *clientConfigLoadingRules) clientConfig {
	return &deferredLoadingClientConfig{loadingRules: loadingRules}
}

func (config *deferredLoadingClientConfig) createClientConfig() (clientConfig, error) {
	if config.clientConfig == nil {
		// REMOVED: Support for concurrent use in multiple threads.
		mergedConfig, err := config.loadingRules.Load()
		if err != nil {
			return nil, err
		}

		var mergedClientConfig clientConfig
		// REMOVED: Interactive fallback support.
		mergedClientConfig = newNonInteractiveClientConfig(*mergedConfig)

		config.clientConfig = mergedClientConfig
	}

	return config.clientConfig, nil
}

// ClientConfig is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.DeferredLoadingClientConfig.ClientConfig.
// ClientConfig implements ClientConfig
func (config *deferredLoadingClientConfig) ClientConfig() (*restConfig, error) {
	mergedClientConfig, err := config.createClientConfig()
	if err != nil {
		return nil, err
	}
	mergedConfig, err := mergedClientConfig.ClientConfig()
	if err != nil {
		return nil, err
	}
	// REMOVED: In-cluster service account configuration use.

	return mergedConfig, nil
}

var (
	// DefaultCluster is the cluster config used when no other config is specified
	// TODO: eventually apiserver should start on 443 and be secure by default
	defaultCluster = clientcmdCluster{Server: "http://localhost:8080"}

	// EnvVarCluster allows overriding the DefaultCluster using an envvar for the server name
	envVarCluster = clientcmdCluster{Server: os.Getenv("KUBERNETES_MASTER")}
)

// directClientConfig is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.DirectClientConfig.
// DirectClientConfig is a ClientConfig interface that is backed by a clientcmdapi.Config, options overrides, and an optional fallbackReader for auth information
type directClientConfig struct {
	config clientcmdConfig
}

// newNonInteractiveClientConfig is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.NewNonInteractiveClientConfig.
// NewNonInteractiveClientConfig creates a DirectClientConfig using the passed context name and does not have a fallback reader for auth information
func newNonInteractiveClientConfig(config clientcmdConfig) clientConfig {
	return &directClientConfig{config}
}

// ClientConfig is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.DirectClientConfig.ClientConfig.
// ClientConfig implements ClientConfig
func (config *directClientConfig) ClientConfig() (*restConfig, error) {
	if err := config.ConfirmUsable(); err != nil {
		return nil, err
	}

	configAuthInfo := config.getAuthInfo()
	configClusterInfo := config.getCluster()

	clientConfig := &restConfig{}
	clientConfig.Host = configClusterInfo.Server
	if u, err := url.ParseRequestURI(clientConfig.Host); err == nil && u.Opaque == "" && len(u.Path) > 1 {
		u.RawQuery = ""
		u.Fragment = ""
		clientConfig.Host = u.String()
	}

	// only try to read the auth information if we are secure
	if isConfigTransportTLS(*clientConfig) {
		var err error

		// mergo is a first write wins for map value and a last writing wins for interface values
		// NOTE: This behavior changed with https://github.com/imdario/mergo/commit/d304790b2ed594794496464fadd89d2bb266600a.
		//       Our mergo.Merge version is older than this change.
		// REMOVED: Support for interactive fallback.
		userAuthPartialConfig, err := getUserIdentificationPartialConfig(configAuthInfo)
		if err != nil {
			return nil, err
		}
		mergo.Merge(clientConfig, userAuthPartialConfig)

		serverAuthPartialConfig, err := getServerIdentificationPartialConfig(configAuthInfo, configClusterInfo)
		if err != nil {
			return nil, err
		}
		mergo.Merge(clientConfig, serverAuthPartialConfig)
	}

	return clientConfig, nil
}

// getServerIdentificationPartialConfig is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.getServerIdentificationPartialConfig.
// clientauth.Info object contain both user identification and server identification.  We want different precedence orders for
// both, so we have to split the objects and merge them separately
// we want this order of precedence for the server identification
// 1.  configClusterInfo (the final result of command line flags and merged .kubeconfig files)
// 2.  configAuthInfo.auth-path (this file can contain information that conflicts with #1, and we want #1 to win the priority)
// 3.  load the ~/.kubernetes_auth file as a default
func getServerIdentificationPartialConfig(configAuthInfo clientcmdAuthInfo, configClusterInfo clientcmdCluster) (*restConfig, error) {
	mergedConfig := &restConfig{}

	// configClusterInfo holds the information identify the server provided by .kubeconfig
	configClientConfig := &restConfig{}
	configClientConfig.CAFile = configClusterInfo.CertificateAuthority
	configClientConfig.CAData = configClusterInfo.CertificateAuthorityData
	configClientConfig.Insecure = configClusterInfo.InsecureSkipTLSVerify
	mergo.Merge(mergedConfig, configClientConfig)

	return mergedConfig, nil
}

// getUserIdentificationPartialConfig is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.getUserIdentificationPartialConfig.
// clientauth.Info object contain both user identification and server identification.  We want different precedence orders for
// both, so we have to split the objects and merge them separately
// we want this order of precedence for user identifcation
// 1.  configAuthInfo minus auth-path (the final result of command line flags and merged .kubeconfig files)
// 2.  configAuthInfo.auth-path (this file can contain information that conflicts with #1, and we want #1 to win the priority)
// 3.  if there is not enough information to idenfity the user, load try the ~/.kubernetes_auth file
// 4.  if there is not enough information to identify the user, prompt if possible
func getUserIdentificationPartialConfig(configAuthInfo clientcmdAuthInfo) (*restConfig, error) {
	mergedConfig := &restConfig{}

	// blindly overwrite existing values based on precedence
	if len(configAuthInfo.Token) > 0 {
		mergedConfig.BearerToken = configAuthInfo.Token
	}
	if len(configAuthInfo.ClientCertificate) > 0 || len(configAuthInfo.ClientCertificateData) > 0 {
		mergedConfig.CertFile = configAuthInfo.ClientCertificate
		mergedConfig.CertData = configAuthInfo.ClientCertificateData
		mergedConfig.KeyFile = configAuthInfo.ClientKey
		mergedConfig.KeyData = configAuthInfo.ClientKeyData
	}
	if len(configAuthInfo.Username) > 0 || len(configAuthInfo.Password) > 0 {
		mergedConfig.Username = configAuthInfo.Username
		mergedConfig.Password = configAuthInfo.Password
	}

	// REMOVED: prompting for missing information.
	return mergedConfig, nil
}

// canIdentifyUser is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.canIdentifyUser
func canIdentifyUser(config restConfig) bool {
	return len(config.Username) > 0 ||
		(len(config.CertFile) > 0 || len(config.CertData) > 0) ||
		len(config.BearerToken) > 0

}

// ConfirmUsable is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.DirectClientConfig.ConfirmUsable.
// ConfirmUsable looks a particular context and determines if that particular part of the config is useable.  There might still be errors in the config,
// but no errors in the sections requested or referenced.  It does not return early so that it can find as many errors as possible.
func (config *directClientConfig) ConfirmUsable() error {
	var validationErrors []error
	validationErrors = append(validationErrors, validateAuthInfo(config.getAuthInfoName(), config.getAuthInfo())...)
	validationErrors = append(validationErrors, validateClusterInfo(config.getClusterName(), config.getCluster())...)
	// when direct client config is specified, and our only error is that no server is defined, we should
	// return a standard "no config" error
	if len(validationErrors) == 1 && validationErrors[0] == errEmptyCluster {
		return newErrConfigurationInvalid([]error{errEmptyConfig})
	}
	return newErrConfigurationInvalid(validationErrors)
}

// getContextName is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.DirectClientConfig.getContextName.
func (config *directClientConfig) getContextName() string {
	// REMOVED: overrides support
	return config.config.CurrentContext
}

// getAuthInfoName is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.DirectClientConfig.getAuthInfoName.
func (config *directClientConfig) getAuthInfoName() string {
	// REMOVED: overrides support
	return config.getContext().AuthInfo
}

// getClusterName is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.DirectClientConfig.getClusterName.
func (config *directClientConfig) getClusterName() string {
	// REMOVED: overrides support
	return config.getContext().Cluster
}

// getContext is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.DirectClientConfig.getContext.
func (config *directClientConfig) getContext() clientcmdContext {
	contexts := config.config.Contexts
	contextName := config.getContextName()

	var mergedContext clientcmdContext
	if configContext, exists := contexts[contextName]; exists {
		mergo.Merge(&mergedContext, configContext)
	}
	// REMOVED: overrides support

	return mergedContext
}

var (
	errEmptyConfig = errors.New("no configuration has been provided")
	// message is for consistency with old behavior
	errEmptyCluster = errors.New("cluster has no server defined")
)

// validateClusterInfo is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.DirectClientConfig.validateClusterInfo.
// validateClusterInfo looks for conflicts and errors in the cluster info
func validateClusterInfo(clusterName string, clusterInfo clientcmdCluster) []error {
	var validationErrors []error

	if reflect.DeepEqual(clientcmdCluster{}, clusterInfo) {
		return []error{errEmptyCluster}
	}

	if len(clusterInfo.Server) == 0 {
		if len(clusterName) == 0 {
			validationErrors = append(validationErrors, fmt.Errorf("default cluster has no server defined"))
		} else {
			validationErrors = append(validationErrors, fmt.Errorf("no server found for cluster %q", clusterName))
		}
	}
	// Make sure CA data and CA file aren't both specified
	if len(clusterInfo.CertificateAuthority) != 0 && len(clusterInfo.CertificateAuthorityData) != 0 {
		validationErrors = append(validationErrors, fmt.Errorf("certificate-authority-data and certificate-authority are both specified for %v. certificate-authority-data will override", clusterName))
	}
	if len(clusterInfo.CertificateAuthority) != 0 {
		clientCertCA, err := os.Open(clusterInfo.CertificateAuthority)
		defer clientCertCA.Close()
		if err != nil {
			validationErrors = append(validationErrors, fmt.Errorf("unable to read certificate-authority %v for %v due to %v", clusterInfo.CertificateAuthority, clusterName, err))
		}
	}

	return validationErrors
}

// validateAuthInfo is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.DirectClientConfig.validateAuthInfo.
// validateAuthInfo looks for conflicts and errors in the auth info
func validateAuthInfo(authInfoName string, authInfo clientcmdAuthInfo) []error {
	var validationErrors []error

	usingAuthPath := false
	methods := make([]string, 0, 3)
	if len(authInfo.Token) != 0 {
		methods = append(methods, "token")
	}
	if len(authInfo.Username) != 0 || len(authInfo.Password) != 0 {
		methods = append(methods, "basicAuth")
	}

	if len(authInfo.ClientCertificate) != 0 || len(authInfo.ClientCertificateData) != 0 {
		// Make sure cert data and file aren't both specified
		if len(authInfo.ClientCertificate) != 0 && len(authInfo.ClientCertificateData) != 0 {
			validationErrors = append(validationErrors, fmt.Errorf("client-cert-data and client-cert are both specified for %v. client-cert-data will override", authInfoName))
		}
		// Make sure key data and file aren't both specified
		if len(authInfo.ClientKey) != 0 && len(authInfo.ClientKeyData) != 0 {
			validationErrors = append(validationErrors, fmt.Errorf("client-key-data and client-key are both specified for %v; client-key-data will override", authInfoName))
		}
		// Make sure a key is specified
		if len(authInfo.ClientKey) == 0 && len(authInfo.ClientKeyData) == 0 {
			validationErrors = append(validationErrors, fmt.Errorf("client-key-data or client-key must be specified for %v to use the clientCert authentication method", authInfoName))
		}

		if len(authInfo.ClientCertificate) != 0 {
			clientCertFile, err := os.Open(authInfo.ClientCertificate)
			defer clientCertFile.Close()
			if err != nil {
				validationErrors = append(validationErrors, fmt.Errorf("unable to read client-cert %v for %v due to %v", authInfo.ClientCertificate, authInfoName, err))
			}
		}
		if len(authInfo.ClientKey) != 0 {
			clientKeyFile, err := os.Open(authInfo.ClientKey)
			defer clientKeyFile.Close()
			if err != nil {
				validationErrors = append(validationErrors, fmt.Errorf("unable to read client-key %v for %v due to %v", authInfo.ClientKey, authInfoName, err))
			}
		}
	}

	// authPath also provides information for the client to identify the server, so allow multiple auth methods in that case
	if (len(methods) > 1) && (!usingAuthPath) {
		validationErrors = append(validationErrors, fmt.Errorf("more than one authentication method found for %v; found %v, only one is allowed", authInfoName, methods))
	}

	return validationErrors
}

// getAuthInfo is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.DirectClientConfig.getAuthInfo.
func (config *directClientConfig) getAuthInfo() clientcmdAuthInfo {
	authInfos := config.config.AuthInfos
	authInfoName := config.getAuthInfoName()

	var mergedAuthInfo clientcmdAuthInfo
	if configAuthInfo, exists := authInfos[authInfoName]; exists {
		mergo.Merge(&mergedAuthInfo, configAuthInfo)
	}
	// REMOVED: overrides support

	return mergedAuthInfo
}

// getCluster is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.DirectClientConfig.getCluster.
func (config *directClientConfig) getCluster() clientcmdCluster {
	clusterInfos := config.config.Clusters
	clusterInfoName := config.getClusterName()

	var mergedClusterInfo clientcmdCluster
	mergo.Merge(&mergedClusterInfo, defaultCluster)
	mergo.Merge(&mergedClusterInfo, envVarCluster)
	if configClusterInfo, exists := clusterInfos[clusterInfoName]; exists {
		mergo.Merge(&mergedClusterInfo, configClusterInfo)
	}
	// REMOVED: overrides support

	return mergedClusterInfo
}

// errConfigurationInvalid is a modified? copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.errConfigurationInvalid.
// errConfigurationInvalid is a set of errors indicating the configuration is invalid.
type errConfigurationInvalid []error

var _ error = errConfigurationInvalid{}

// REMOVED: utilerrors.Aggregate implementation for errConfigurationInvalid.

// newErrConfigurationInvalid is a modified? copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.newErrConfigurationInvalid.
func newErrConfigurationInvalid(errs []error) error {
	switch len(errs) {
	case 0:
		return nil
	default:
		return errConfigurationInvalid(errs)
	}
}

// Error implements the error interface
func (e errConfigurationInvalid) Error() string {
	return fmt.Sprintf("invalid configuration: %v", utilerrors.NewAggregate(e).Error())
}

// clientConfigLoadingRules is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.ClientConfigLoadingRules
// ClientConfigLoadingRules is an ExplicitPath and string slice of specific locations that are used for merging together a Config
// Callers can put the chain together however they want, but we'd recommend:
// EnvVarPathFiles if set (a list of files if set) OR the HomeDirectoryPath
// ExplicitPath is special, because if a user specifically requests a certain file be used and error is reported if thie file is not present
type clientConfigLoadingRules struct {
	Precedence []string
}

// Load is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.ClientConfigLoadingRules.Load
// Load starts by running the MigrationRules and then
// takes the loading rules and returns a Config object based on following rules.
//   if the ExplicitPath, return the unmerged explicit file
//   Otherwise, return a merged config based on the Precedence slice
// A missing ExplicitPath file produces an error. Empty filenames or other missing files are ignored.
// Read errors or files with non-deserializable content produce errors.
// The first file to set a particular map key wins and map key's value is never changed.
// BUT, if you set a struct value that is NOT contained inside of map, the value WILL be changed.
// This results in some odd looking logic to merge in one direction, merge in the other, and then merge the two.
// It also means that if two files specify a "red-user", only values from the first file's red-user are used.  Even
// non-conflicting entries from the second file's "red-user" are discarded.
// Relative paths inside of the .kubeconfig files are resolved against the .kubeconfig file's parent folder
// and only absolute file paths are returned.
func (rules *clientConfigLoadingRules) Load() (*clientcmdConfig, error) {
	errlist := []error{}

	kubeConfigFiles := []string{}

	// REMOVED: explicit path support
	kubeConfigFiles = append(kubeConfigFiles, rules.Precedence...)

	kubeconfigs := []*clientcmdConfig{}
	// read and cache the config files so that we only look at them once
	for _, filename := range kubeConfigFiles {
		if len(filename) == 0 {
			// no work to do
			continue
		}

		config, err := loadFromFile(filename)
		if os.IsNotExist(err) {
			// skip missing files
			continue
		}
		if err != nil {
			errlist = append(errlist, fmt.Errorf("Error loading config file \"%s\": %v", filename, err))
			continue
		}

		kubeconfigs = append(kubeconfigs, config)
	}

	// first merge all of our maps
	mapConfig := clientcmdNewConfig()
	for _, kubeconfig := range kubeconfigs {
		mergo.Merge(mapConfig, kubeconfig)
	}

	// merge all of the struct values in the reverse order so that priority is given correctly
	// errors are not added to the list the second time
	nonMapConfig := clientcmdNewConfig()
	for i := len(kubeconfigs) - 1; i >= 0; i-- {
		kubeconfig := kubeconfigs[i]
		mergo.Merge(nonMapConfig, kubeconfig)
	}

	// since values are overwritten, but maps values are not, we can merge the non-map config on top of the map config and
	// get the values we expect.
	config := clientcmdNewConfig()
	mergo.Merge(config, mapConfig)
	mergo.Merge(config, nonMapConfig)

	// REMOVED: Possibility to skip this.
	if err := resolveLocalPaths(config); err != nil {
		errlist = append(errlist, err)
	}

	return config, utilerrors.NewAggregate(errlist)
}

// loadFromFile is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.LoadFromFile
// LoadFromFile takes a filename and deserializes the contents into Config object
func loadFromFile(filename string) (*clientcmdConfig, error) {
	kubeconfigBytes, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	config, err := load(kubeconfigBytes)
	if err != nil {
		return nil, err
	}

	// set LocationOfOrigin on every Cluster, User, and Context
	for key, obj := range config.AuthInfos {
		obj.LocationOfOrigin = filename
		config.AuthInfos[key] = obj
	}
	for key, obj := range config.Clusters {
		obj.LocationOfOrigin = filename
		config.Clusters[key] = obj
	}
	for key, obj := range config.Contexts {
		obj.LocationOfOrigin = filename
		config.Contexts[key] = obj
	}

	if config.AuthInfos == nil {
		config.AuthInfos = map[string]*clientcmdAuthInfo{}
	}
	if config.Clusters == nil {
		config.Clusters = map[string]*clientcmdCluster{}
	}
	if config.Contexts == nil {
		config.Contexts = map[string]*clientcmdContext{}
	}

	return config, nil
}

// load is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.Load
// Load takes a byte slice and deserializes the contents into Config object.
// Encapsulates deserialization without assuming the source is a file.
func load(data []byte) (*clientcmdConfig, error) {
	config := clientcmdNewConfig()
	// if there's no data in a file, return the default object instead of failing (DecodeInto reject empty input)
	if len(data) == 0 {
		return config, nil
	}
	// Note: This does absolutely no kind/version checking or conversions.
	data, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, config); err != nil {
		return nil, err
	}
	return config, nil
}

// resolveLocalPaths is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.ClientConfigLoadingRules.resolveLocalPaths.
// ResolveLocalPaths resolves all relative paths in the config object with respect to the stanza's LocationOfOrigin
// this cannot be done directly inside of LoadFromFile because doing so there would make it impossible to load a file without
// modification of its contents.
func resolveLocalPaths(config *clientcmdConfig) error {
	for _, cluster := range config.Clusters {
		if len(cluster.LocationOfOrigin) == 0 {
			continue
		}
		base, err := filepath.Abs(filepath.Dir(cluster.LocationOfOrigin))
		if err != nil {
			return fmt.Errorf("Could not determine the absolute path of config file %s: %v", cluster.LocationOfOrigin, err)
		}

		if err := resolvePaths(getClusterFileReferences(cluster), base); err != nil {
			return err
		}
	}
	for _, authInfo := range config.AuthInfos {
		if len(authInfo.LocationOfOrigin) == 0 {
			continue
		}
		base, err := filepath.Abs(filepath.Dir(authInfo.LocationOfOrigin))
		if err != nil {
			return fmt.Errorf("Could not determine the absolute path of config file %s: %v", authInfo.LocationOfOrigin, err)
		}

		if err := resolvePaths(getAuthInfoFileReferences(authInfo), base); err != nil {
			return err
		}
	}

	return nil
}

// getClusterFileReferences is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.ClientConfigLoadingRules.GetClusterFileReferences.
func getClusterFileReferences(cluster *clientcmdCluster) []*string {
	return []*string{&cluster.CertificateAuthority}
}

// getAuthInfoFileReferences is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.ClientConfigLoadingRules.GetAuthInfoFileReferences.
func getAuthInfoFileReferences(authInfo *clientcmdAuthInfo) []*string {
	return []*string{&authInfo.ClientCertificate, &authInfo.ClientKey}
}

// resolvePaths is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd.ClientConfigLoadingRules.resolvePaths.
// ResolvePaths updates the given refs to be absolute paths, relative to the given base directory
func resolvePaths(refs []*string, base string) error {
	for _, ref := range refs {
		// Don't resolve empty paths
		if len(*ref) > 0 {
			// Don't resolve absolute paths
			if !filepath.IsAbs(*ref) {
				*ref = filepath.Join(base, *ref)
			}
		}
	}
	return nil
}

// restClientFor is a modified copy of k8s.io/kubernets/pkg/client/restclient.RESTClientFor.
// RESTClientFor returns a RESTClient that satisfies the requested attributes on a client Config
// object. Note that a RESTClient may require fields that are optional when initializing a Client.
// A RESTClient created by this method is generic - it expects to operate on an API that follows
// the Kubernetes conventions, but may not be the Kubernetes API.
func restClientFor(config *restConfig) (*url.URL, *http.Client, error) {
	// REMOVED: Configurable GroupVersion, Codec
	// REMOVED: Configurable versionedAPIPath
	baseURL, err := defaultServerURLFor(config)
	if err != nil {
		return nil, nil, err
	}

	transport, err := transportFor(config)
	if err != nil {
		return nil, nil, err
	}

	var httpClient *http.Client
	if transport != http.DefaultTransport {
		httpClient = &http.Client{Transport: transport}
	}

	// REMOVED: Configurable QPS, Burst, ContentConfig
	// REMOVED: Actually returning a RESTClient object.
	return baseURL, httpClient, nil
}

// defaultServerURL is a modified copy of k8s.io/kubernets/pkg/client/restclient.DefaultServerURL.
// DefaultServerURL converts a host, host:port, or URL string to the default base server API path
// to use with a Client at a given API version following the standard conventions for a
// Kubernetes API.
func defaultServerURL(host string, defaultTLS bool) (*url.URL, error) {
	if host == "" {
		return nil, fmt.Errorf("host must be a URL or a host:port pair")
	}
	base := host
	hostURL, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	if hostURL.Scheme == "" {
		scheme := "http://"
		if defaultTLS {
			scheme = "https://"
		}
		hostURL, err = url.Parse(scheme + base)
		if err != nil {
			return nil, err
		}
		if hostURL.Path != "" && hostURL.Path != "/" {
			return nil, fmt.Errorf("host must be a URL or a host:port pair: %q", base)
		}
	}

	// REMOVED: versionedAPIPath computation.
	return hostURL, nil
}

// defaultServerURLFor is a modified copy of k8s.io/kubernets/pkg/client/restclient.defaultServerURLFor.
// defaultServerUrlFor is shared between IsConfigTransportTLS and RESTClientFor. It
// requires Host and Version to be set prior to being called.
func defaultServerURLFor(config *restConfig) (*url.URL, error) {
	// TODO: move the default to secure when the apiserver supports TLS by default
	// config.Insecure is taken to mean "I want HTTPS but don't bother checking the certs against a CA."
	hasCA := len(config.CAFile) != 0 || len(config.CAData) != 0
	hasCert := len(config.CertFile) != 0 || len(config.CertData) != 0
	defaultTLS := hasCA || hasCert || config.Insecure
	host := config.Host
	if host == "" {
		host = "localhost"
	}

	// REMOVED: Configurable APIPath, GroupVersion
	return defaultServerURL(host, defaultTLS)
}

// transportFor is a modified copy of k8s.io/kubernets/pkg/client/restclient.transportFor.
// TransportFor returns an http.RoundTripper that will provide the authentication
// or transport level security defined by the provided Config. Will return the
// default http.DefaultTransport if no special case behavior is needed.
func transportFor(config *restConfig) (http.RoundTripper, error) {
	// REMOVED: separation between restclient.Config and transport.Config, Transport, WrapTransport support
	return transportNew(config)
}

// isConfigTransportTLS is a modified copy of k8s.io/kubernets/pkg/client/restclient.IsConfigTransportTLS.
// IsConfigTransportTLS returns true if and only if the provided
// config will result in a protected connection to the server when it
// is passed to restclient.RESTClientFor().  Use to determine when to
// send credentials over the wire.
//
// Note: the Insecure flag is ignored when testing for this value, so MITM attacks are
// still possible.
func isConfigTransportTLS(config restConfig) bool {
	baseURL, err := defaultServerURLFor(&config)
	if err != nil {
		return false
	}
	return baseURL.Scheme == "https"
}

// transportNew is a modified copy of k8s.io/kubernetes/pkg/client/transport.New.
// New returns an http.RoundTripper that will provide the authentication
// or transport level security defined by the provided Config.
func transportNew(config *restConfig) (http.RoundTripper, error) {
	// REMOVED: custom config.Transport support.
	// Set transport level security

	var (
		rt  http.RoundTripper
		err error
	)

	rt, err = tlsCacheGet(config)
	if err != nil {
		return nil, err
	}

	// REMOVED: HTTPWrappersForConfig(config, rt) in favor of the caller setting HTTP headers itself based on restConfig. Only this inlined check remains.
	if len(config.Username) != 0 && len(config.BearerToken) != 0 {
		return nil, fmt.Errorf("username/password or bearer token may be set, but not both")
	}

	return rt, nil
}

// tlsCacheGet is a modified copy of k8s.io/kubernetes/pkg/client/transport.tlsTransportCache.get.
func tlsCacheGet(config *restConfig) (http.RoundTripper, error) {
	// REMOVED: any actual caching

	// Get the TLS options for this client config
	tlsConfig, err := tlsConfigFor(config)
	if err != nil {
		return nil, err
	}
	// The options didn't require a custom TLS config
	if tlsConfig == nil {
		return http.DefaultTransport, nil
	}

	return utilnet.SetTransportDefaults(&http.Transport{ // FIXME??
		Proxy:               http.ProxyFromEnvironment,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig:     tlsConfig,
		Dial: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).Dial,
	}), nil
}

// tlsConfigFor is a modified copy of k8s.io/kubernetes/pkg/client/transport.TLSConfigFor.
// TLSConfigFor returns a tls.Config that will provide the transport level security defined
// by the provided Config. Will return nil if no transport level security is requested.
func tlsConfigFor(c *restConfig) (*tls.Config, error) {
	if !(c.HasCA() || c.HasCertAuth() || c.Insecure) {
		return nil, nil
	}
	if c.HasCA() && c.Insecure {
		return nil, fmt.Errorf("specifying a root certificates file with the insecure flag is not allowed")
	}
	if err := loadTLSFiles(c); err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{
		// Change default from SSLv3 to TLSv1.0 (because of POODLE vulnerability)
		MinVersion:         tls.VersionTLS10,
		InsecureSkipVerify: c.Insecure,
	}

	if c.HasCA() {
		tlsConfig.RootCAs = rootCertPool(c.CAData)
	}

	if c.HasCertAuth() {
		cert, err := tls.X509KeyPair(c.CertData, c.KeyData)
		if err != nil {
			return nil, err
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	return tlsConfig, nil
}

// loadTLSFiles is a modified copy of k8s.io/kubernetes/pkg/client/transport.loadTLSFiles.
// loadTLSFiles copies the data from the CertFile, KeyFile, and CAFile fields into the CertData,
// KeyData, and CAFile fields, or returns an error. If no error is returned, all three fields are
// either populated or were empty to start.
func loadTLSFiles(c *restConfig) error {
	var err error
	c.CAData, err = dataFromSliceOrFile(c.CAData, c.CAFile)
	if err != nil {
		return err
	}

	c.CertData, err = dataFromSliceOrFile(c.CertData, c.CertFile)
	if err != nil {
		return err
	}

	c.KeyData, err = dataFromSliceOrFile(c.KeyData, c.KeyFile)
	if err != nil {
		return err
	}
	return nil
}

// dataFromSliceOrFile is a modified copy of k8s.io/kubernetes/pkg/client/transport.dataFromSliceOrFile.
// dataFromSliceOrFile returns data from the slice (if non-empty), or from the file,
// or an error if an error occurred reading the file
func dataFromSliceOrFile(data []byte, file string) ([]byte, error) {
	if len(data) > 0 {
		return data, nil
	}
	if len(file) > 0 {
		fileData, err := ioutil.ReadFile(file)
		if err != nil {
			return []byte{}, err
		}
		return fileData, nil
	}
	return nil, nil
}

// rootCertPool is a modified copy of k8s.io/kubernetes/pkg/client/transport.rootCertPool.
// rootCertPool returns nil if caData is empty.  When passed along, this will mean "use system CAs".
// When caData is not empty, it will be the ONLY information used in the CertPool.
func rootCertPool(caData []byte) *x509.CertPool {
	// What we really want is a copy of x509.systemRootsPool, but that isn't exposed.  It's difficult to build (see the go
	// code for a look at the platform specific insanity), so we'll use the fact that RootCAs == nil gives us the system values
	// It doesn't allow trusting either/or, but hopefully that won't be an issue
	if len(caData) == 0 {
		return nil
	}

	// if we have caData, use it
	certPool := x509.NewCertPool()
	certPool.AppendCertsFromPEM(caData)
	return certPool
}

// HasCA is a modified copy of k8s.io/kubernetes/pkg/client/transport.Config.HasCA.
// HasCA returns whether the configuration has a certificate authority or not.
func (c *restConfig) HasCA() bool {
	return len(c.CAData) > 0 || len(c.CAFile) > 0
}

// HasCertAuth is a modified copy of k8s.io/kubernetes/pkg/client/transport.Config.HasCertAuth.
// HasCertAuth returns whether the configuration has certificate authentication or not.
func (c *restConfig) HasCertAuth() bool {
	return len(c.CertData) != 0 || len(c.CertFile) != 0
}

// clientcmdConfig is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd/api.Config.
// Config holds the information needed to build connect to remote kubernetes clusters as a given user
// IMPORTANT if you add fields to this struct, please update IsConfigEmpty()
type clientcmdConfig struct {
	// Clusters is a map of referencable names to cluster configs
	Clusters clustersMap `json:"clusters"`
	// AuthInfos is a map of referencable names to user configs
	AuthInfos authInfosMap `json:"users"`
	// Contexts is a map of referencable names to context configs
	Contexts contextsMap `json:"contexts"`
	// CurrentContext is the name of the context that you would like to use by default
	CurrentContext string `json:"current-context"`
}

type clustersMap map[string]*clientcmdCluster

func (m *clustersMap) UnmarshalJSON(data []byte) error {
	var a []v1NamedCluster
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	for _, e := range a {
		cluster := e.Cluster // Allocates a new instance in each iteration
		(*m)[e.Name] = &cluster
	}
	return nil
}

type authInfosMap map[string]*clientcmdAuthInfo

func (m *authInfosMap) UnmarshalJSON(data []byte) error {
	var a []v1NamedAuthInfo
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	for _, e := range a {
		authInfo := e.AuthInfo // Allocates a new instance in each iteration
		(*m)[e.Name] = &authInfo
	}
	return nil
}

type contextsMap map[string]*clientcmdContext

func (m *contextsMap) UnmarshalJSON(data []byte) error {
	var a []v1NamedContext
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	for _, e := range a {
		context := e.Context // Allocates a new instance in each iteration
		(*m)[e.Name] = &context
	}
	return nil
}

// clientcmdNewConfig is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd/api.NewConfig.
// NewConfig is a convenience function that returns a new Config object with non-nil maps
func clientcmdNewConfig() *clientcmdConfig {
	return &clientcmdConfig{
		Clusters:  make(map[string]*clientcmdCluster),
		AuthInfos: make(map[string]*clientcmdAuthInfo),
		Contexts:  make(map[string]*clientcmdContext),
	}
}

// clientcmdCluster is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd/api.Cluster.
// Cluster contains information about how to communicate with a kubernetes cluster
type clientcmdCluster struct {
	// LocationOfOrigin indicates where this object came from.  It is used for round tripping config post-merge, but never serialized.
	LocationOfOrigin string
	// Server is the address of the kubernetes cluster (https://hostname:port).
	Server string `json:"server"`
	// InsecureSkipTLSVerify skips the validity check for the server's certificate. This will make your HTTPS connections insecure.
	InsecureSkipTLSVerify bool `json:"insecure-skip-tls-verify,omitempty"`
	// CertificateAuthority is the path to a cert file for the certificate authority.
	CertificateAuthority string `json:"certificate-authority,omitempty"`
	// CertificateAuthorityData contains PEM-encoded certificate authority certificates. Overrides CertificateAuthority
	CertificateAuthorityData []byte `json:"certificate-authority-data,omitempty"`
}

// clientcmdAuthInfo is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd/api.AuthInfo.
// AuthInfo contains information that describes identity information.  This is use to tell the kubernetes cluster who you are.
type clientcmdAuthInfo struct {
	// LocationOfOrigin indicates where this object came from.  It is used for round tripping config post-merge, but never serialized.
	LocationOfOrigin string
	// ClientCertificate is the path to a client cert file for TLS.
	ClientCertificate string `json:"client-certificate,omitempty"`
	// ClientCertificateData contains PEM-encoded data from a client cert file for TLS. Overrides ClientCertificate
	ClientCertificateData []byte `json:"client-certificate-data,omitempty"`
	// ClientKey is the path to a client key file for TLS.
	ClientKey string `json:"client-key,omitempty"`
	// ClientKeyData contains PEM-encoded data from a client key file for TLS. Overrides ClientKey
	ClientKeyData []byte `json:"client-key-data,omitempty"`
	// Token is the bearer token for authentication to the kubernetes cluster.
	Token string `json:"token,omitempty"`
	// Username is the username for basic authentication to the kubernetes cluster.
	Username string `json:"username,omitempty"`
	// Password is the password for basic authentication to the kubernetes cluster.
	Password string `json:"password,omitempty"`
}

// clientcmdContext is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd/api.Context.
// Context is a tuple of references to a cluster (how do I communicate with a kubernetes cluster), a user (how do I identify myself), and a namespace (what subset of resources do I want to work with)
type clientcmdContext struct {
	// LocationOfOrigin indicates where this object came from.  It is used for round tripping config post-merge, but never serialized.
	LocationOfOrigin string
	// Cluster is the name of the cluster for this context
	Cluster string `json:"cluster"`
	// AuthInfo is the name of the authInfo for this context
	AuthInfo string `json:"user"`
	// Namespace is the default namespace to use on unspecified requests
	Namespace string `json:"namespace,omitempty"`
}

// v1NamedCluster is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd/api.v1.NamedCluster.
// NamedCluster relates nicknames to cluster information
type v1NamedCluster struct {
	// Name is the nickname for this Cluster
	Name string `json:"name"`
	// Cluster holds the cluster information
	Cluster clientcmdCluster `json:"cluster"`
}

// v1NamedContext is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd/api.v1.NamedContext.
// NamedContext relates nicknames to context information
type v1NamedContext struct {
	// Name is the nickname for this Context
	Name string `json:"name"`
	// Context holds the context information
	Context clientcmdContext `json:"context"`
}

// v1NamedAuthInfo is a modified copy of k8s.io/kubernetes/pkg/client/unversioned/clientcmd/api.v1.NamedAuthInfo.
// NamedAuthInfo relates nicknames to auth information
type v1NamedAuthInfo struct {
	// Name is the nickname for this AuthInfo
	Name string `json:"name"`
	// AuthInfo holds the auth information
	AuthInfo clientcmdAuthInfo `json:"user"`
}
