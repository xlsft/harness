Types into whatever has the keyboard focus on a booted simulator, as a hardware keyboard would: tap the field first. `text` is typed as is (a newline presses Return); characters a US keyboard has no key for are pasted through the device's pasteboard. `keys` presses combinations after the text, space separated, such as `cmd+a delete` or `return`. Key names: return, escape, delete, tab, space, up, down, left, right, home, end, page_up, page_down, forward_delete; modifiers cmd, ctrl, alt, shift.

There is no check for password fields on the device. Type only test data: never a real password, token or personal detail of the user. If a sign-in needs real credentials, ask the user to type them in the Simulator pane.

The on-screen keyboard may hide while a hardware keyboard types; that is normal.
