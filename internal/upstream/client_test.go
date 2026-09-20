package upstream

import (
	"net/http"
	"testing"
)

func TestClassify_ModelDenied(t *testing.T) {
	// 上游对套餐未开放的模型返回 403 model_not_allowed：
	// 这是请求侧问题，必须归到 ErrModelDenied，不能当成账号故障。
	body := `{"error":{"code":"model_not_allowed","message":"Model is not allowed for this plan."}}`
	if got := Classify(http.StatusForbidden, body); got != ErrModelDenied {
		t.Errorf("Classify(403, model_not_allowed) = %v, want %v", got, ErrModelDenied)
	}
}

func TestClassify_ForbiddenOtherwiseIsClient(t *testing.T) {
	// 其他 403（不含模型关键词）仍按普通 4xx 处理。
	if got := Classify(http.StatusForbidden, `{"error":{"code":"forbidden"}}`); got != ErrClient {
		t.Errorf("Classify(403, forbidden) = %v, want %v", got, ErrClient)
	}
}

func TestClassify_KnownKinds(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   ErrKind
	}{
		{http.StatusPaymentRequired, `{}`, ErrHardCredit},
		{http.StatusOK, `{"error":{"message":"积分不足"}}`, ErrHardCredit},
		{http.StatusTooManyRequests, `{}`, ErrSoftRate},
		{http.StatusUnauthorized, `{}`, ErrSessionDead},
		{http.StatusNotFound, `{}`, ErrNotFound},
		{http.StatusInternalServerError, `{}`, ErrServer},
		{http.StatusBadRequest, `{}`, ErrClient},
		{http.StatusOK, `{}`, ErrNone},
	}
	for _, c := range cases {
		if got := Classify(c.status, c.body); got != c.want {
			t.Errorf("Classify(%d, %q) = %v, want %v", c.status, c.body, got, c.want)
		}
	}
}
