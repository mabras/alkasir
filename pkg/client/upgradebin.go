package client

import (
	"fmt"
	"net/url"
	"path"
	"runtime"
	"sync"
	"time"

	"github.com/alkasir/alkasir/pkg/service"
	"github.com/alkasir/alkasir/pkg/shared"
	"github.com/alkasir/alkasir/pkg/upgradebin"
	"github.com/hashicorp/go-version"
	"github.com/thomasf/lg"
)

var (
	artifactNameMu sync.Mutex
	artifactName   string
)

func SetUpgradeArtifact(name string) {
	artifactNameMu.Lock()
	artifactName = fmt.Sprintf("%s-%s-%s", name, runtime.GOOS, runtime.GOARCH)
	artifactNameMu.Unlock()
}

// StartBinaryUpgradeChecker checks for binary upgrades when the connection is
// up and on a schedule.
//
// This function runs in it's own goroutine.
func StartBinaryUpgradeChecker(diffsBaseURL string) {
	if !upgradeEnabled {
		lg.Infoln("binary upgrades are disabled using the command line flag")
		return
	}
	if VERSION == "" {
		lg.Warningln("VERSION not set, binary upgrades are disabled")
		return
	}
	_, err := version.NewVersion(VERSION)
	if err != nil {
		lg.Warningf("VERSION '%s' is not a valid semver version, binary upgrades are disabled: %v", VERSION, err)
		return
	}

	connectionEventListener := make(chan service.ConnectionHistory)
	uChecker, _ := NewUpdateChecker("binary")
	service.AddListener(connectionEventListener)
	for {
		select {
		// Update when the transport connection comes up
		case event := <-connectionEventListener:
			if event.IsUp() {
				uChecker.Activate()
				uChecker.UpdateNow()
			}

		// Update by request of the update checker
		case request := <-uChecker.RequestC:
			err := upgradeBinaryCheck(diffsBaseURL)
			if err != nil {
				lg.Errorln(err)
				request.ResponseC <- UpdateError
			} else {
				request.ResponseC <- UpdateSuccess
			}
		}
	}
}

func upgradeBinaryCheck(diffsBaseURL string) error {
	artifactNameMu.Lock()
	artifact := artifactName
	artifactNameMu.Unlock()

	cl, err := NewRestClient()
	if err != nil {
		return err
	}

	res, found, err := cl.CheckBinaryUpgrade(shared.BinaryUpgradeRequest{
		Artifact:    artifact,
		FromVersion: VERSION,
	})
	if err != nil {
		return err
	}
	if !found {
		lg.Infoln("no update found")
		return nil
	}
	lg.Warningf("found update %+v", res)

	httpclient, err := service.NewTransportHTTPClient(2 * time.Hour)
	if err != nil {
		return err
	}

	opts, err := upgradebin.NewUpdaterOptions(res, shared.UpgradeVerificationPublicKey)
	if err != nil {
		return err
	}

	u, err := url.Parse(diffsBaseURL)
	if err != nil {
		lg.Errorln(err)
		return err
	}
	u.Path = path.Join(u.Path, artifact, VERSION, res.Version)
	URL := u.String()

	lg.Infoln("downloading", URL)
	resp, err := httpclient.Get(URL)
	if err != nil {
		lg.Errorln(err)
		return err
	}

	defer resp.Body.Close()
	err = upgradebin.Apply(resp.Body, opts)
	if err != nil {
		lg.Errorln(err)
		// will be retried the next time the client starts
		return nil
	}

	return nil

}
