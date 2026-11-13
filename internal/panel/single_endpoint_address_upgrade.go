package panel

import (
	"context"
	"database/sql"
	"fmt"
)

// Rebuild old address snapshots after the key is available and before serving
// reconnecting nodes. The durable marker survives an interrupted panel upgrade.
func (p *Panel) upgradeEndpointAddresses(ctx context.Context) error {
	var pending bool
	if err := p.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM node_snapshots WHERE address_upgrade_pending=1)`).Scan(&pending); err != nil {
		return err
	}
	if !pending {
		return nil
	}
	if err := p.commitForwarding(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE node_snapshots SET address_upgrade_pending=0 WHERE address_upgrade_pending=1`)
		return err
	}); err != nil {
		return fmt.Errorf("升级端点地址快照: %w", err)
	}
	return nil
}
