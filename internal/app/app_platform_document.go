package app

import "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"

// The complete derived document stays private until current authorization and
// trust have been rechecked after compilation. The future HTTP owner must send
// its CSP and no-store policy; this repository registers no route.
func (s *Server) readPlatformLaunchDocument(session platformActorSession, expected platformLaunchAccess) (appplatform.FrontendDocument, error) {
	var result appplatform.FrontendDocument
	_, err := s.withPlatformLaunchRead(session, expected.installationID, func(e migExec, current platformLaunchAccess, m appplatform.Manifest) error {
		if current != expected {
			return errAppPlatformLaunchUnavailable
		}
		var archive []byte
		if err := e.queryRow(`SELECT content FROM app_platform_frontend_artifacts WHERE sha256=?`, current.frontendSHA).Scan(&archive); err != nil {
			return err
		}
		var err error
		result, err = appplatform.BuildFrontendDocument(m, archive)
		return err
	})
	if err != nil {
		return appplatform.FrontendDocument{}, err
	}
	return result, nil
}
