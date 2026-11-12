package core

// Split is the payout rule, in integer micro-USDC.
//
//   - Everyone who checked in gets their deposit back plus an equal share of
//     the forfeited deposits.
//   - Anyone who did not check in gets nothing back.
//   - The remainder of the integer division (at most shows-1 micro-USDC)
//     stays in the DAO.
//   - If nobody checked in, every deposit stays in the DAO.
type SplitResult struct {
	Pot           uint64 `json:"pot"`
	Forfeit       uint64 `json:"forfeit"`
	SharePerShow  uint64 `json:"share_per_show"`
	PayoutPerShow uint64 `json:"payout_per_show"`
	Dust          uint64 `json:"dust"`
	ToDAO         uint64 `json:"to_dao"`
}

// Split computes the payout for going RSVPs of which shows checked in.
func Split(deposit uint64, going, shows uint32) SplitResult {
	if shows > going {
		shows = going
	}
	r := SplitResult{Pot: deposit * uint64(going)}
	r.Forfeit = deposit * uint64(going-shows)
	if shows == 0 {
		r.ToDAO = r.Pot
		return r
	}
	r.SharePerShow = r.Forfeit / uint64(shows)
	r.Dust = r.Forfeit % uint64(shows)
	r.PayoutPerShow = deposit + r.SharePerShow
	r.ToDAO = r.Dust
	return r
}
