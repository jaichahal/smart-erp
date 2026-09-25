// Package bank matches statement lines to the cash book and keeps petty cash, cheques, and the daily position.
// Matching a line does not allocate a receipt. Allocating a receipt does not create a bank movement.
// Post-dated cheques stay on role pdc_in until deposit, so they are not bank cash.
package bank
