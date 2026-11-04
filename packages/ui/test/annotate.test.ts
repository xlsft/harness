import { describe, expect, it } from 'vitest'
import { arrowHead, contrastOn, drawsSomething, flatten, nextStep, pixelBounds, savedName, savedType, type Painter, type Shape } from '~/lib/session/annotate'

// A tiny raster: drawImage fills it with the image's marker and fillRect paints whole pixels, enough to see what is left
// of the image under a redaction.
function raster(width: number, height: number) {
  const px = Array.from({ length: height }, () => Array.from({ length: width }, () => 'none'))
  const calls: string[] = []
  const ctx = {
    fillStyle: '', strokeStyle: '', lineWidth: 1, lineCap: 'butt', lineJoin: 'miter', font: '', textAlign: 'left', textBaseline: 'top', globalAlpha: 1, globalCompositeOperation: 'source-over',
    save() {},
    restore() {},
    beginPath() {},
    moveTo() {},
    lineTo() {},
    closePath() {},
    stroke() {
      calls.push('stroke')
    },
    fill() {
      calls.push('fill')
    },
    arc() {},
    ellipse() {
      calls.push('ellipse')
    },
    strokeRect() {
      calls.push('strokeRect')
    },
    clearRect() {},
    fillText(t: string) {
      calls.push(`text:${t}`)
    },
    drawImage() {
      calls.push('image')
      px.forEach(row => row.fill('image'))
    },
    fillRect(x: number, y: number, w: number, h: number) {
      calls.push('fillRect')
      expect([x, y, w, h].every(Number.isInteger)).toBe(true)
      expect(this.globalAlpha).toBe(1)
      for (let j = y; j < y + h; j++) for (let i = x; i < x + w; i++) px[j]![i] = String(this.fillStyle)
    }
  }
  return { ctx: ctx as unknown as Painter, px, calls }
}
const image = {} as CanvasImageSource

describe('flattening', () => {
  it('paints the image at its own size, then the shapes in order', () => {
    const { ctx, calls } = raster(10, 10)
    const shapes: Shape[] = [
      { kind: 'rect', color: '#ef4444', width: 2, from: { x: 1, y: 1 }, to: { x: 5, y: 5 } },
      { kind: 'text', color: '#000000', at: { x: 0, y: 0 }, text: 'a\nb', size: 10, font: '10px mono' },
      { kind: 'step', color: '#3b82f6', at: { x: 5, y: 5 }, n: 3, radius: 4 }
    ]
    flatten(ctx, image, { width: 10, height: 10 }, shapes)
    expect(calls).toEqual(['image', 'strokeRect', 'text:a', 'text:b', 'fill', 'text:3'])
  })

  it('leaves no pixel of the image under a redaction, fractional edges included', () => {
    const { ctx, px } = raster(6, 4)
    flatten(ctx, image, { width: 6, height: 4 }, [{ kind: 'redact', color: '#000000', width: 1, from: { x: 3.6, y: 0.4 }, to: { x: 1.2, y: 2.5 } }])
    expect(px).toEqual([
      ['image', '#000000', '#000000', '#000000', 'image', 'image'],
      ['image', '#000000', '#000000', '#000000', 'image', 'image'],
      ['image', '#000000', '#000000', '#000000', 'image', 'image'],
      ['image', 'image', 'image', 'image', 'image', 'image']
    ])
  })

  it('cuts a redaction dragged past the edge to the image', () => {
    expect(pixelBounds({ x: -5, y: -5 }, { x: 100.5, y: 3 }, 20, 10)).toEqual({ x: 0, y: 0, w: 20, h: 3 })
    expect(pixelBounds({ x: 30, y: 30 }, { x: 40, y: 40 }, 20, 10)).toEqual({ x: 30, y: 30, w: 0, h: 0 })
  })
})

describe('shapes', () => {
  it('counts steps up, and an undone one gives its number back', () => {
    const step = (n: number): Shape => ({ kind: 'step', color: '#000000', at: { x: 0, y: 0 }, n, radius: 4 })
    const line: Shape = { kind: 'line', color: '#000000', width: 1, from: { x: 0, y: 0 }, to: { x: 9, y: 9 } }
    expect(nextStep([])).toBe(1)
    expect(nextStep([step(1), line, step(2)])).toBe(3)
    expect(nextStep([step(1), line])).toBe(2)
  })

  it('points the arrow head at the end of the drag, and stops the shaft inside it', () => {
    const head = arrowHead({ x: 0, y: 0 }, { x: 100, y: 0 }, 4)
    expect(head.tip).toEqual({ x: 100, y: 0 })
    expect(head.left.x).toBeLessThan(100)
    expect(head.left.y).toBeCloseTo(-head.right.y)
    expect(head.shaftEnd.x).toBeLessThan(100)
    expect(head.shaftEnd.x).toBeGreaterThan(head.left.x)
  })

  it('never makes the head longer than a short arrow', () => {
    const head = arrowHead({ x: 0, y: 0 }, { x: 3, y: 0 }, 8)
    expect(Math.hypot(head.left.x - 3, head.left.y)).toBeCloseTo(3)
  })

  it('reads a step number on any colour', () => {
    expect(contrastOn('#ffffff')).toBe('#000000')
    expect(contrastOn('#000000')).toBe('#ffffff')
    expect(contrastOn('#3b82f6')).toBe('#ffffff')
  })

  it('drops a click with a drag tool', () => {
    const at = { x: 5, y: 5 }
    expect(drawsSomething({ kind: 'rect', color: '', width: 1, from: at, to: at })).toBe(false)
    expect(drawsSomething({ kind: 'redact', color: '', width: 1, from: at, to: { x: 5, y: 9 } })).toBe(true)
    expect(drawsSomething({ kind: 'pen', color: '', width: 1, points: [at] })).toBe(true)
    expect(drawsSomething({ kind: 'text', color: '', at, text: '  ', size: 1, font: '' })).toBe(false)
  })
})

describe('the saved file', () => {
  it('keeps a photo a photo, and the rest lossless', () => {
    expect(savedType('image/jpeg')).toBe('image/jpeg')
    expect(savedType('image/webp')).toBe('image/webp')
    expect(savedType('image/png')).toBe('image/png')
    expect(savedType('image/gif')).toBe('image/png')
  })

  it('names the file after what was saved', () => {
    expect(savedName('a.png', 'image/png')).toBe('a.png')
    expect(savedName('a.JPG', 'image/jpeg')).toBe('a.JPG')
    expect(savedName('a.gif', 'image/png')).toBe('a.png')
    expect(savedName('a.webp', 'image/png')).toBe('a.png')
    expect(savedName('noext', 'image/png')).toBe('noext.png')
  })
})
