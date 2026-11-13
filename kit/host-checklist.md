# Host checklist: running Doorlist at a real meetup

Print this. Tick as you go. The same list is in the app at `/kit`.

## A week before

- [ ] Pick the hosts who form the DAO council (two or three people who will be there).
- [ ] Create the community's Sputnik DAO: `scripts/create-dao.sh`. It gives the Doorlist contract a `doorlist` role limited to Transfer proposals.
- [ ] Register the DAO with the USDC contract (the script does it) so it can hold USDC.
- [ ] Create the event: `scripts/create-event.sh`. Suggested: 5 to 10 USDC deposit, check-in open for 3 hours, a 24-hour head start for regulars from the second event on.
- [ ] Do one full dry run with a friend: RSVP, scan, close, payout.
- [ ] Share the guest link. Say plainly what happens to a no-show's deposit.

## The day before

- [ ] Print the poster (`/e/<event>/poster`, A4) twice: entrance and bar.
- [ ] Charge the door phone and a spare. Check the venue has signal or Wi-Fi at the door.
- [ ] Top up the relayer account with a little NEAR for gas.

## On the night

- [ ] An hour before start: open the door scanner on the door phone, sign in, register the phone (one wallet signature).
- [ ] Scan one test ticket before the queue forms.
- [ ] Scan every ticket. Coral "IN." means done. If the camera fails, type the six-letter code under the QR.
- [ ] "Not on the list" usually means the deposit hasn't settled: ask the guest to open their ticket page and wait for it.
- [ ] Walk-ins can scan the poster and RSVP on the spot until the start time.
- [ ] Tell people at the door: "Your deposit is already marked for refund. If people flake tonight, you get their share."

## After

- [ ] Close the event from the host page. Payouts file and approve themselves.
- [ ] Open the DAO and check that the number of approved Transfer proposals equals the number of check-ins.
- [ ] Post the result: N RSVPs, M showed up, X USDC moved from no-shows to the people who came.
- [ ] Write down what broke. Open an issue.

## If something goes wrong

| Problem | Do this |
|---|---|
| Door phone died | Open the door page on any phone and register it (up to 8 doors per event) |
| No signal at the door | Note names on paper, move to signal, type the codes before check-in closes |
| A payout shows "paying" for long | Open the DAO: the proposal is there; a council member can approve it |
| Event cancelled | `cancel_event` before the start time: every deposit is owed back in full |
