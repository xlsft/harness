// Session cost: reads the session through the message API of xlsft`s harness (window.harness, version 1) and draws what it
// has spent. It asks for two capabilities, both declared in .harness-plugin/plugin.json: session.read and theme.read. The
// app asks the user the first time each is used; until then a call fails with the code "denied" or waits for the answer.
(function () {
  var $ = function (id) { return document.getElementById(id) }
  var points = []
  var accent = 'currentColor'

  function usd(n) { return '$' + (n < 1 ? n.toFixed(4) : n.toFixed(2)) }
  function count(n) { return Number(n || 0).toLocaleString('en-US') }

  function draw() {
    var svg = $('spark')
    if (points.length < 2) { svg.innerHTML = ''; return }
    var max = Math.max.apply(null, points) || 1
    var step = 200 / (points.length - 1)
    var d = points.map(function (v, i) { return (i ? 'L' : 'M') + (i * step).toFixed(1) + ' ' + (38 - (v / max) * 36).toFixed(1) }).join(' ')
    svg.innerHTML = '<path d="' + d + '" fill="none" stroke-width="1.5" vector-effect="non-scaling-stroke"></path>'
    svg.firstChild.setAttribute('stroke', accent)
  }

  function show(s) {
    $('total').textContent = usd(s.costUsd || 0)
    $('input').textContent = count(s.inputTokens)
    $('output').textContent = count(s.outputTokens)
    $('cache-read').textContent = count(s.cacheReadTokens)
    $('cache-write').textContent = count(s.cacheWriteTokens)
    $('last').textContent = s.lastTurn ? usd(s.lastTurn.costUsd) + ', ' + count(s.lastTurn.outputTokens) + ' tokens out' : '-'
    $('model').textContent = s.model || '-'
    if (points[points.length - 1] !== s.costUsd) points.push(s.costUsd || 0)
    if (points.length > 60) points.shift()
    draw()
  }

  function fail(e) {
    var note = $('note')
    note.className = 'note problem'
    note.textContent = e && e.code === 'denied' ? 'Allow this plugin to read the session to see its cost.' : String((e && e.message) || e)
  }

  harness.call('theme.read').then(function (t) { accent = t['--accent'] || accent; draw() }).catch(function () {})
  harness.call('session.read').then(function (s) {
    show(s)
    harness.on('session', show)
  }).catch(fail)
})()
