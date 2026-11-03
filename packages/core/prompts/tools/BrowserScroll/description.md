Scrolls the active tab. Either scroll by a distance: `direction` (up, down, left or right) and an optional `amount` in CSS pixels (default: most of the viewport); add `ref` or `x`/`y` to scroll whatever is under that point, such as a scrollable list or panel, instead of the page. Or bring an element into view: `ref` without `direction`.

- The result gives the new scroll position, the page height and the viewport height, and says when the bottom is reached.
- BrowserClick and BrowserType scroll their target into view by themselves. Scroll to reveal content for a snapshot or screenshot, or to make lazy lists load more.
