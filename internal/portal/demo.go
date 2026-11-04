package portal

import (
	"net/http"
	"time"
)

func (a *App) demoPlans(w http.ResponseWriter, r *http.Request) error {
	if e := requireOwner(r); e != nil {
		return e
	}
	count, e := a.SeedDemoPlans(r)
	if e != nil {
		return e
	}
	send(w, 200, map[string]any{"created": count, "message": "示例套餐已准备；不会覆盖已有编辑，模拟套餐不接受真实下单"})
	return nil
}
func (a *App) SeedDemoPlans(r *http.Request) (int, error) {
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx); e != nil {
		return 0, e
	}
	now := a.now()
	end := now.AddDate(5, 0, 0)
	created := 0
	for ci, cat := range []struct {
		Name, Description string
		Plans             []struct {
			Name               string
			GB, Price, Devices int
		}
	}{
		{"轻量体验", "适合轻度浏览与偶尔出行；示例价格，可编辑", []struct {
			Name               string
			GB, Price, Devices int
		}{{"轻享 20G", 20, 500, 2}, {"轻享 50G", 50, 900, 2}}},
		{"日常畅连", "适合日常多端使用；示例价格，可编辑", []struct {
			Name               string
			GB, Price, Devices int
		}{{"标准 100G", 100, 1500, 3}, {"标准 200G", 200, 2500, 3}}},
		{"大流量精选", "适合高流量需求；示例价格，可编辑", []struct {
			Name               string
			GB, Price, Devices int
		}{{"畅连 300G", 300, 3500, 5}, {"畅连 500G", 500, 5500, 5}}},
	} {
		category := hash("starhub-portal-demo-category/" + cat.Name)[:32]
		if _, e = tx.Exec(ctx, `INSERT INTO categories(id,name,description,sort) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO NOTHING`, category, cat.Name, cat.Description, (ci+1)*10); e != nil {
			return 0, e
		}
		for pi, p := range cat.Plans {
			id := hash("starhub-portal-demo-plan/" + p.Name)[:32]
			prices := []Price{{ID: "MONTH", Months: 1, Amount: int64(p.Price), Enabled: true}, {ID: "QUARTER", Months: 3, Amount: int64(p.Price * 27 / 10), Enabled: true}, {ID: "YEAR", Months: 12, Amount: int64(p.Price * 10), Enabled: true}}
			tag, err := tx.Exec(ctx, `INSERT INTO plans(id,category_id,name,description,enabled,recommended,sort,starts_at,ends_at,recommend_starts_at,recommend_ends_at,quota_bytes,group_ids,protocols,prices,max_devices,billing_mode,multiplier_milli,allow_new,allow_renew,simulation) VALUES($1,$2,$3,$4,true,$5,$6,$7,$8,$7,$8,$9,'[]',$10,$11,$12,'both',1000,true,true,true) ON CONFLICT(id) DO NOTHING`, id, category, p.Name, "模拟展示套餐；请编辑绑定可计费用户组、确认价格后关闭模拟标记再售卖。", ci == 1, (ci+1)*10+pi, now.Add(-time.Hour), end, int64(p.GB)*1000000000, raw([]string{"anytls", "hysteria2"}), raw(prices), p.Devices)
			if err != nil {
				return 0, err
			}
			created += int(tag.RowsAffected())
		}
	}
	if e = audit(ctx, tx, current(r).ID, "demo-plans.seed", "catalog"); e != nil {
		return 0, e
	}
	if e = tx.Commit(ctx); e != nil {
		return 0, e
	}
	return created, nil
}
