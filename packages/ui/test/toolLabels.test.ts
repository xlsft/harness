import { describe, expect, it } from 'vitest'
import type { ToolItem } from '~/lib/session/types'
import { bashCwdNote, editedPath, fileChanges, notebookCellLabel, toolIcon, toolMeta, toolTarget, withoutCwdNote } from '~/utils/tools'

const tool = (o: Partial<ToolItem>) => ({ id: 't', name: 'Bash', input: {}, status: 'ok', summary: '', resultSummary: '', durationMs: 0, startedAt: 0, ...o }) as ToolItem

describe('Bash cwd note', () => {
  const note = 'Shell now stays in /p/sub.'

  it('comes from the structured result', () => {
    expect(bashCwdNote(tool({ result: { stdout: '', cwdNote: note } }))).toBe(note)
    expect(bashCwdNote(tool({ result: { stdout: '' } }))).toBe('')
    expect(bashCwdNote(tool({}))).toBe('')
  })

  it('is taken off the end of a text that carries it', () => {
    expect(withoutCwdNote(`boom\nExit code 1\n${note}`, note)).toBe('boom\nExit code 1')
    expect(withoutCwdNote(note, note)).toBe('')
    expect(withoutCwdNote('plain\n', '')).toBe('plain\n')
    expect(withoutCwdNote(`${note}\nthen more`, note)).toBe(`${note}\nthen more`)
  })
})

describe('NotebookEdit row', () => {
  const edit = tool({
    name: 'NotebookEdit',
    input: { notebook_path: '/p/a.ipynb', cell_id: 'calc', new_source: 'x' },
    result: { filePath: '/p/a.ipynb', editMode: 'replace', cellId: 'calc', cellIndex: 1, structuredPatch: [{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 2, lines: ['-a', '+b', '+c'] }] }
  })

  it('names the notebook and the cell', () => {
    expect(toolTarget(edit)).toBe('/p/a.ipynb')
    expect(editedPath(edit)).toBe('/p/a.ipynb')
    expect(notebookCellLabel(edit)).toBe('replace cell calc')
    expect(notebookCellLabel(tool({ name: 'NotebookEdit', result: { editMode: 'insert', cellId: '', cellIndex: 0 } }))).toBe('insert cell 0')
    expect(toolIcon(edit)).toBe('edit')
  })

  it('counts as an edit of the file', () => {
    expect(toolMeta(edit, 0).text).toBe('+2 −1')
    expect(fileChanges([edit])).toEqual([{ kind: 'M', path: '/p/a.ipynb', add: 2, del: 1 }])
  })
})

describe('Monitor row', () => {
  it('says what it watches', () => {
    expect(toolTarget(tool({ name: 'Monitor', input: { task_id: 'b1', pattern: 'ERROR' } }))).toBe('task b1 /ERROR/')
    expect(toolTarget(tool({ name: 'Monitor', input: { command: 'npm run dev', pattern: 'ready' } }))).toBe('npm run dev /ready/')
    expect(toolMeta(tool({ name: 'Monitor', result: { persistent: true } }), 0).text).toBe('watching')
    expect(toolIcon(tool({ name: 'Monitor' }))).toBe('clock')
  })
})
