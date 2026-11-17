package app

import "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"

// readPlatformLaunchAsset repeats current reader authorization and verifies the
// requested binding inside the transaction that reads immutable artifact bytes.
// It is a server-owned buffered read, not an HTTP handler or an app bridge API.
func (s *Server) readPlatformLaunchAsset(session platformActorSession, expected platformLaunchAccess, name string) (appplatform.FrontendFile, error) {
	var result appplatform.FrontendFile
	_, err := s.withPlatformLaunchRead(session, expected.installationID, func(e migExec, current platformLaunchAccess, m appplatform.Manifest) error {
		if current != expected {
			return errAppPlatformLaunchUnavailable
		}
		var archive []byte
		if err := e.queryRow(`SELECT content FROM app_platform_frontend_artifacts WHERE sha256=?`, current.frontendSHA).Scan(&archive); err != nil {
			return err
		}
		var err error
		result, err = appplatform.ReadFrontendFile(m, archive, name)
		return err
	})
	if err != nil {
		return appplatform.FrontendFile{}, err
	}
	return result, nil
}
