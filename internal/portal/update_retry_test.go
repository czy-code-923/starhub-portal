package portal

import (
	"context"
	"testing"
)

func TestFailedVersionUpdateCanRetryWithoutDuplicateJob(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	release := map[string]any{"tag_name": "v99.0.0", "html_url": "https://github.com/" + Repository + "/releases/tag/v99.0.0", "draft": false, "prerelease": false}
	if _, e := f.a.DB.Exec(ctx, "INSERT INTO release_cache(id,payload,checked_at) VALUES(true,$1,now())", raw(release)); e != nil {
		t.Fatal(e)
	}
	if _, e := f.a.DB.Exec(ctx, "INSERT INTO local_worker(id,last_seen,status) VALUES(true,now(),'{}')"); e != nil {
		t.Fatal(e)
	}
	body := map[string]string{"version": "99.0.0", "current": Version}
	w := f.req("POST", "admin/version", body, &f.admin, true)
	expect(t, w, 202)
	id := object(t, w)["id"].(string)
	expect(t, f.req("POST", "admin/version", body, &f.admin, true), 409)
	for _, state := range []string{"failed", "rolled_back"} {
		if _, e := f.a.DB.Exec(ctx, "UPDATE jobs SET state=$1,attempts=3,detail='temporary fetch failure' WHERE id=$2", state, id); e != nil {
			t.Fatal(e)
		}
		w = f.req("POST", "admin/version", body, &f.admin, true)
		expect(t, w, 202)
		if object(t, w)["id"] != id {
			t.Fatal("created a duplicate version task")
		}
		var queued string
		var attempts int
		if e := f.a.DB.QueryRow(ctx, "SELECT state,attempts FROM jobs WHERE id=$1", id).Scan(&queued, &attempts); e != nil {
			t.Fatal(e)
		}
		if queued != "queued" || attempts != 0 {
			t.Fatal(queued, attempts)
		}
	}
	if _, e := f.a.DB.Exec(ctx, "UPDATE jobs SET state='succeeded' WHERE id=$1", id); e != nil {
		t.Fatal(e)
	}
	expect(t, f.req("POST", "admin/version", body, &f.admin, true), 409)
}
