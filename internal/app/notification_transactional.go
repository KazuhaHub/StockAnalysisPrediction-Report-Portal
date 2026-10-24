package app

import (
	"context"
	"log"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/notification"
)

// submitTransactionalMail keeps authentication mail outside operational routing
// and never waits on a recipient's SMTP server in a public authentication request.
func (s *Server) submitTransactionalMail(task func(context.Context)) {
	s.mailExecMu.Lock()
	if s.mailExec == nil {
		s.mailExec = notification.NewExecutor(2, 32)
	}
	ex := s.mailExec
	s.mailExecMu.Unlock()
	if !ex.Submit(task) {
		log.Print("notification: transactional mail capacity unavailable")
	}
}

func (s *Server) stopTransactionalMail(ctx context.Context) {
	s.mailExecMu.Lock()
	if s.mailExec == nil {
		s.mailExec = notification.NewExecutor(2, 32)
	}
	ex := s.mailExec
	s.mailExecMu.Unlock()
	if err := ex.Close(ctx); err != nil {
		log.Print("notification: transactional shutdown deadline exceeded")
	}
}
