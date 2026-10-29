package service

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/util/common"
)

// MTLSKeyPair is the panel's own ECDSA P-256 keypair used for mTLS federation.
// The private key and the self-signed CA certificate are generated once per
// panel and persisted in the settings table; nodes pin the CA cert so the hub
// is provably itself on the wire.
type MTLSKeyPair struct {
	PrivatePem []byte `json:"privatePem"`
	CAcertPem  []byte `json:"caCertPem"`
	SubjectCN  string `json:"subjectCN"`
	CreatedAt  int64  `json:"createdAt"`
}

const mTLSStorageKey = "mTLSKeyPair"

// MTLSGenerate creates (or returns the existing) panel mTLS keypair. The key
// pair is generated once per panel and persisted. Regenerated only if absent.
func (s *SettingService) MTLSGenerate() (*MTLSKeyPair, error) {
	existing, err := s.MTLSLoad()
	if err == nil && existing != nil {
		return existing, nil
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, common.NewError("generating mTLS private key: ", err.Error())
	}

	privDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, common.NewError("marshaling private key: ", err.Error())
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privDER})

	hubCN := "vpnui-hub"
	if name, _ := s.GetServerName(); strings.TrimSpace(name) != "" {
		hubCN = strings.TrimSpace(name)
	}

	caPEM, err := s.buildSelfSignedCert(priv, hubCN)
	if err != nil {
		return nil, err
	}

	kp := &MTLSKeyPair{
		PrivatePem: privPEM,
		CAcertPem:  caPEM,
		SubjectCN:  hubCN,
		CreatedAt:  time.Now().UnixMilli(),
	}
	if err := s.MTLSSave(kp); err != nil {
		return nil, err
	}
	logger.Info("mTLS keypair generated, subject CN:", hubCN)
	return kp, nil
}

// MTLSSave persists the keypair.
func (s *SettingService) MTLSSave(kp *MTLSKeyPair) error {
	b, err := json.Marshal(kp)
	if err != nil {
		return err
	}
	return s.setString(mTLSStorageKey, string(b))
}

// MTLSLoad returns the persisted keypair, or nil when none has been generated.
func (s *SettingService) MTLSLoad() (*MTLSKeyPair, error) {
	val, err := s.getString(mTLSStorageKey)
	if err != nil {
		return nil, err
	}
	if val == "" {
		return nil, nil
	}
	var kp MTLSKeyPair
	if err := json.Unmarshal([]byte(val), &kp); err != nil {
		return nil, err
	}
	if len(kp.PrivatePem) == 0 || len(kp.CAcertPem) == 0 {
		return nil, nil
	}
	return &kp, nil
}

// MTLSCA returns the panel's self-signed CA certificate, generating the key
// pair on first use.
func (s *SettingService) MTLSCA() ([]byte, error) {
	kp, err := s.MTLSGenerate()
	if err != nil {
		return nil, err
	}
	return kp.CAcertPem, nil
}

// MTLSCertForHub returns the panel's CA cert and the subject CN it signs with.
func (s *SettingService) MTLSCertForHub() ([]byte, string, error) {
	kp, err := s.MTLSGenerate()
	if err != nil {
		return nil, "", err
	}
	return kp.CAcertPem, kp.SubjectCN, nil
}

// buildSelfSignedCert creates a 10-year self-signed CA cert for the panel.
func (s *SettingService) buildSelfSignedCert(priv *ecdsa.PrivateKey, subjectCN string) ([]byte, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		serial = big.NewInt(time.Now().UnixNano())
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   subjectCN,
			Organization: []string{"vpn-ui"},
		},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().AddDate(10, 0, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IsCA:        true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, common.NewError("building self-signed mTLS cert: ", err.Error())
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// SignClientCert issues a node client certificate signed by this panel's CA,
// for the subject CN named. The node presents it on every mTLS dial and the
// panel's TLS layer verifies it against the CA bundle.
//
// The panel ships the CA + key so the operator CAN build a PKI; the hub still
// names itself via X-Hub-Name on the wire as before, and the panel's TLS
// server accepts any cert whose chain reaches the panel's own CA.
func (s *SettingService) SignClientCert(subjectCN string, ttl time.Duration) ([]byte, error) {
	kp, err := s.MTLSGenerate()
	if err != nil {
		return nil, err
	}

	privBlock, _ := pem.Decode(kp.PrivatePem)
	if privBlock == nil {
		return nil, common.NewError("parsing mTLS private key: no PEM block")
	}
	priv, err := x509.ParseECPrivateKey(privBlock.Bytes)
	if err != nil {
		return nil, common.NewError("parsing mTLS EC private key: ", err.Error())
	}

	caBlock, _ := pem.Decode(kp.CAcertPem)
	if caBlock == nil {
		return nil, common.NewError("parsing mTLS CA cert: no PEM block")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return nil, common.NewError("parsing mTLS CA cert: ", err.Error())
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: subjectCN, Organization: []string{"vpn-ui"}},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(ttl),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	leaf, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &priv.PublicKey, priv)
	if err != nil {
		return nil, common.NewError("signing client cert: ", err.Error())
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf}), nil
}

// EnsureMtlsCertsInitialized verifies that an mTLS keypair exists and is ready
// to use. Called once at startup; if missing, it generates one. Idempotent:
// a second call returns the existing keypair without re-generating.
func (s *SettingService) EnsureMtlsCertsInitialized() error {
	_, err := s.MTLSGenerate()
	return err
}

// MTLSCertBundle returns the panel's CA cert(s), the exact byte blob a node's
// TLS client should load as its CA bundle when dialing the panel over mTLS.
func (s *SettingService) MTLSCertBundle() ([]byte, error) {
	ca, err := s.MTLSCA()
	if err != nil {
		return nil, err
	}
	return ca, nil
}

// GetOrCreateMtlsCert is a back-compat alias for MTLSCertBundle, kept so any
// caller compiled against the pre-refactor name still resolves.
func (s *SettingService) GetOrCreateMtlsCert() ([]byte, error) {
	return s.MTLSCertBundle()
}
