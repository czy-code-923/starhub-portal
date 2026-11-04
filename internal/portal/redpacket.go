package portal

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"golang.org/x/text/unicode/norm"
)

func normalizeRedpacket(value string) (string, error) {
	value = strings.ToUpper(norm.NFKC.String(strings.Join(strings.Fields(value), "")))
	if utf8.RuneCountInString(value) < 4 || utf8.RuneCountInString(value) > 128 || !cleanText(value, 128) {
		return "", bad("请填写 4–128 个字符的真实红包口令，不要填写支付宝密码或验证码")
	}
	return value, nil
}
func (a *App) saveRedpacket(ctx context.Context, tx pgx.Tx, order, value string) ([]byte, error) {
	value, e := normalizeRedpacket(value)
	if e != nil {
		return nil, e
	}
	codeHash := a.csrf("redpacket/" + value)
	var claimed string
	e = tx.QueryRow(ctx, `INSERT INTO redpacket_claims(code_hash,order_id) VALUES($1,$2) ON CONFLICT(code_hash) DO UPDATE SET code_hash=EXCLUDED.code_hash RETURNING order_id`, codeHash, order).Scan(&claimed)
	if e != nil {
		return nil, e
	}
	if claimed != order {
		return nil, Problem{409, "此口令已绑定另一订单，请勿重复提交同一红包"}
	}
	return a.seal(map[string]string{"password": value})
}
func (a *App) redpacketValue(ctx context.Context, order string) (string, error) {
	var sealed []byte
	if e := a.DB.QueryRow(ctx, `SELECT redpacket_sealed FROM manual_payments WHERE order_id=$1 AND method_id='alipay_redpacket' AND redpacket_sealed IS NOT NULL`, order).Scan(&sealed); e != nil {
		return "", e
	}
	var data map[string]string
	if e := a.unseal(sealed, &data); e != nil {
		return "", e
	}
	return data["password"], nil
}
func (a *App) viewRedpacket(w http.ResponseWriter, r *http.Request) error {
	if e := requireAdminScope(r, "payments"); e != nil {
		return e
	}
	var order string
	if e := a.DB.QueryRow(r.Context(), "SELECT order_id FROM manual_payments WHERE id=$1", r.PathValue("id")).Scan(&order); e != nil {
		return e
	}
	value, e := a.redpacketValue(r.Context(), order)
	if e != nil {
		return e
	}
	if _, e = a.DB.Exec(r.Context(), "INSERT INTO audit(actor,action,subject) VALUES($1,'payment.redpacket.view',$2)", current(r).ID, order); e != nil {
		return e
	}
	send(w, 200, map[string]string{"password": value, "order_id": order})
	return nil
}
