package promauth

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/pkg/apis/clientauthentication"
	"sigs.k8s.io/yaml"

	clientauthenticationv1 "k8s.io/client-go/pkg/apis/clientauthentication/v1"
	clientauthenticationv1beta1 "k8s.io/client-go/pkg/apis/clientauthentication/v1beta1"
)

const (
	// NeverExecInteractiveMode declares that this exec plugin never needs to use standard
	// input, and therefore the exec plugin will be run regardless of whether standard input is
	// available for user input.
	NeverExecInteractiveMode string = "Never"
	// IfAvailableExecInteractiveMode declares that this exec plugin would like to use standard input
	// if it is available, but can still operate if standard input is not available. Therefore, the
	// exec plugin will be run regardless of whether stdin is available for user input. If standard
	// input is available for user input, then it will be provided to this exec plugin.
	IfAvailableExecInteractiveMode string = "IfAvailable"
	// AlwaysExecInteractiveMode declares that this exec plugin requires standard input in order to
	// run, and therefore the exec plugin will only be run if standard input is available for user
	// input. If standard input is not available for user input, then the exec plugin will not be run
	// and an error will be returned by the exec plugin runner.
	AlwaysExecInteractiveMode string = "Always"
)

var (
	// Since transports can be constantly re-initialized by programs like kubectl,
	// keep a cache of initialized execAuthenticators keyed by a hash of their config.
	// The list of API versions we accept.
	apiVersions = map[string]schema.GroupVersion{
		clientauthenticationv1beta1.SchemeGroupVersion.String(): clientauthenticationv1beta1.SchemeGroupVersion,
		clientauthenticationv1.SchemeGroupVersion.String():      clientauthenticationv1.SchemeGroupVersion,
	}
	expireTimeOffset = 5 * time.Second
)

func isInteractive(isTerminalFunc func(int) bool, config *ExecConfig) (bool, error) {
	var shouldBeInteractive bool
	switch config.InteractiveMode {
	case NeverExecInteractiveMode:
		shouldBeInteractive = false
	case IfAvailableExecInteractiveMode:
		shouldBeInteractive = !config.StdinUnavailable && isTerminalFunc(int(os.Stdin.Fd()))
	case AlwaysExecInteractiveMode:
		if !isTerminalFunc(int(os.Stdin.Fd())) {
			return false, errors.New("standard input is not a terminal")
		}
		if config.StdinUnavailable {
			suffix := ""
			if len(config.StdinUnavailableMessage) > 0 {
				// only print extra ": <message>" if the user actually specified a message
				suffix = fmt.Sprintf(": %s", config.StdinUnavailableMessage)
			}
			return false, fmt.Errorf("standard input is unavailable%s", suffix)
		}
		shouldBeInteractive = true
	default:
		return false, fmt.Errorf("unknown interactiveMode: %q", config.InteractiveMode)
	}

	return shouldBeInteractive, nil
}

func (a *execAuthenticator) getCreds() (*credentials, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cachedCreds != nil && a.credsExpired() {
		return a.cachedCreds, nil
	}

	if err := a.refreshCredsLocked(); err != nil {
		return nil, err
	}

	return a.cachedCreds, nil
}

func (a *execAuthenticator) getAuthHeaderFunc() (string, error) {
	creds, err := a.getCreds()
	if err != nil {
		return "", err
	}

	return "Bearer " + creds.token, nil
}

func (a *execAuthenticator) refreshCredsLocked() error {
	interactive, err := a.interactiveFunc()
	if err != nil {
		return fmt.Errorf("exec plugin cannot support interactive mode: %w", err)
	}

	cred := &clientauthentication.ExecCredential{
		Spec: clientauthentication.ExecCredentialSpec{
			Interactive: interactive,
		},
	}

	env := append(a.environ(), a.env...)

	stdout := &bytes.Buffer{}
	cmd := exec.Command(a.cmd, a.args...)
	cmd.Env = env
	cmd.Stderr = a.stderr
	cmd.Stdout = stdout
	if interactive {
		cmd.Stdin = a.stdin
	}
	if interactive {
		cmd.Stdin = a.stdin
	}

	err = cmd.Run()
	if err != nil {
		return a.wrapCmdRunErrorLocked(err)
	}

	if err = a.decodeCredential(stdout.Bytes(), cred); err != nil {
		return fmt.Errorf("error decoding exec plugin response: %w", err)
	}
	if cred.GroupVersionKind().Version != a.group.Version || cred.GroupVersionKind().Group != a.group.Group {
		return fmt.Errorf("exec plugin is configured to use API version %s, plugin returned version %s",
			a.group, schema.GroupVersion{Group: cred.GroupVersionKind().Group, Version: cred.APIVersion})
	}
	if cred.Status == nil {
		return fmt.Errorf("exec plugin didn't return a status field")
	}
	if cred.Status.Token == "" {
		return fmt.Errorf("exec plugin only support token")
	}
	if cred.Status.ExpirationTimestamp != nil {
		// 提前 5 秒钟过期
		a.exp = cred.Status.ExpirationTimestamp.Time.Add(-expireTimeOffset)
	} else {
		a.exp = time.Time{}
	}
	a.cachedCreds = &credentials{
		token: cred.Status.Token,
	}
	return nil
}

// decodeCredential detects the format of the output and decodes it into the credential struct
func (a *execAuthenticator) decodeCredential(output []byte, cred *clientauthentication.ExecCredential) error {
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 {
		return errors.New("empty output from exec plugin")
	}

	// Detect format by checking the first non-whitespace character
	// JSON starts with '{', YAML typically starts with a letter or '-'
	switch trimmed[0] {
	case '{':
		// JSON format
		if err := json.Unmarshal(output, cred); err != nil {
			return fmt.Errorf("JSON unmarshal error: %w", err)
		}
	default:
		// YAML format
		if err := yaml.Unmarshal(output, cred); err != nil {
			return fmt.Errorf("YAML unmarshal error: %w", err)
		}
	}

	return nil
}

// wrapCmdRunErrorLocked pulls out the code to construct a helpful error message
// for when the exec plugin's binary fails to Run().
//
// It must be called while holding the Authenticator's mutex.
func (a *execAuthenticator) wrapCmdRunErrorLocked(err error) error {
	switch err.(type) {
	case *exec.Error: // Binary does not exist (see exec.Error).
		builder := strings.Builder{}
		fmt.Fprintf(&builder, "exec: executable %s not found", a.cmd)
		return errors.New(builder.String())

	case *exec.ExitError: // Binary execution failed (see exec.Cmd.Run()).
		e := err.(*exec.ExitError)
		return fmt.Errorf(
			"exec: executable %s failed with exit code %d",
			a.cmd,
			e.ProcessState.ExitCode(),
		)

	default:
		return fmt.Errorf("exec: %v", err)
	}
}

func (a *execAuthenticator) credsExpired() bool {
	if a.exp.IsZero() {
		return false
	}
	return a.now().After(a.exp)
}

// execAuthenticator is a client credential provider that rotates credentials by executing a plugin.
// The plugin input and output are defined by the API group client.authentication.k8s.io.
type execAuthenticator struct {
	// Set by the config
	cmd         string
	args        []string
	group       schema.GroupVersion
	env         []string
	installHint string

	// Stubbable for testing
	stdin           io.Reader
	stderr          io.Writer
	interactiveFunc func() (bool, error)
	now             func() time.Time
	environ         func() []string

	// Cached results.
	//
	// The mutex also guards calling the plugin. Since the plugin could be
	// interactive we want to make sure it's only called once.
	mu          sync.Mutex
	cachedCreds *credentials
	exp         time.Time
}

type credentials struct {
	token string           `datapolicy:"token"`
	cert  *tls.Certificate `datapolicy:"secret-key"`
}

func (e *ExecConfig) String() string {
	ret := fmt.Sprintf(`exec(command=%q, `, e.Command)
	if len(e.Args) > 0 {
		ret += ", args=" + strings.Join(e.Args, ", ")
	}

	if len(e.Env) > 0 {
		ret += "env("
		for i := range e.Env {
			ret += fmt.Sprintf("%s=%q ", e.Env[i].Name, e.Env[i].Value)
		}
		ret = ret[:len(ret)-2]
		ret += "), "
	}
	if e.InteractiveMode != "" {
		ret += "interactiveMode=" + e.InteractiveMode + ", "
	}
	if e.APIVersion != "" {
		ret += "apiVersion=" + e.APIVersion + ", "
	}
	if e.InstallHint != "" {
		ret += "installHint=" + e.InstallHint + ", "
	}
	ret += ")"
	return ret
}

func (e *ExecConfig) GetAuthToken() (string, error) {
	return e.auth.getAuthHeaderFunc()
}

func (e *ExecConfig) getAuthHeaderFunc() (string, error) {
	return e.auth.getAuthHeaderFunc()
}

func (e *ExecConfig) InitAuthenticator() error {
	if e == nil {
		return errors.New("exec config is nil")
	}

	gv, ok := apiVersions[e.APIVersion]
	if !ok {
		return fmt.Errorf("exec plugin: invalid apiVersion %q", e.APIVersion)
	}

	a := &execAuthenticator{
		cmd:   e.Command,
		args:  e.Args,
		group: gv,

		stdin:           os.Stdin,
		stderr:          os.Stderr,
		interactiveFunc: func() (bool, error) { return isInteractive(term.IsTerminal, e) },
		now:             time.Now,
		environ:         os.Environ,

		installHint: e.InstallHint,
	}
	for _, env := range e.Env {
		a.env = append(a.env, env.Name+"="+env.Value)
	}
	e.auth = a

	return nil
}

type ExecCredential struct {
	metav1.TypeMeta

	// Status is filled in by the plugin and holds the credentials that the transport
	// should use to contact the API.
	// +optional
	Status *ExecCredentialStatus
}

// ExecCredentialStatus holds credentials for the transport to use.
type ExecCredentialStatus struct {
	// ExpirationTimestamp indicates a time when the provided credentials expire.
	// +optional
	ExpirationTimestamp *metav1.Time
	// Token is a bearer token used by the client for request authentication.
	// +optional
	Token string `datapolicy:"token"`
	// PEM-encoded client TLS certificate.
	// +optional
	ClientCertificateData string
	// PEM-encoded client TLS private key.
	// +optional
	ClientKeyData string `datapolicy:"secret-key"`
}

type ExecConfig struct {
	// Command to execute.
	Command string `yaml:"command"`
	// Arguments to pass to the command when executing it.
	Args []string `yaml:"args"`

	// Env defines additional environment variables to expose to the process. These
	// are unioned with the host's environment, as well as variables client-go uses
	// to pass argument to the plugin.
	Env []ExecEnvVar `yaml:"env"`

	// Preferred input version of the ExecInfo. The returned ExecCredentials MUST use
	// the same encoding version as the input.
	APIVersion string `yaml:"apiVersion,omitempty"`

	// This text is shown to the user when the executable doesn't seem to be
	// present. For example, `brew install foo-cli` might be a good InstallHint for
	// foo-cli on Mac OS systems.
	InstallHint string `yaml:"installHint,omitempty"`

	// InteractiveMode determines this plugin's relationship with standard input. Valid
	// values are "Never" (this exec plugin never uses standard input), "IfAvailable" (this
	// exec plugin wants to use standard input if it is available), or "Always" (this exec
	// plugin requires standard input to function). See ExecInteractiveMode values for more
	// details.
	//
	// If APIVersion is client.authentication.k8s.io/v1alpha1 or
	// client.authentication.k8s.io/v1beta1, then this field is optional and defaults
	// to "IfAvailable" when unset. Otherwise, this field is required.
	//+optional
	InteractiveMode string `yaml:"interactiveMode,omitempty"`

	// StdinUnavailable indicates whether the exec execAuthenticator can pass standard
	// input through to this exec plugin. For example, a higher level entity might be using
	// standard input for something else and therefore it would not be safe for the exec
	// plugin to use standard input. This is kept here in order to keep all of the exec configuration
	// together, but it is never serialized.
	// +k8s:conversion-gen=false
	StdinUnavailable bool `yaml:"-"`

	// StdinUnavailableMessage is an optional message to be displayed when the exec execAuthenticator
	// cannot successfully run this exec plugin because it needs to use standard input and
	// StdinUnavailable is true. For example, a process that is already using standard input to
	// read user instructions might set this to "used by my-program to read user instructions".
	// +k8s:conversion-gen=false
	StdinUnavailableMessage string `json:"-"`

	auth *execAuthenticator
}

func (env *ExecEnvVar) String() string {
	return fmt.Sprintf("%s=%s", env.Name, env.Value)
}

// ExecEnvVar is used for setting environment variables when executing an exec-based
// credential plugin.
type ExecEnvVar struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}
