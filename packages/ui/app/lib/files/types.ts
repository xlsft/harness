// FilesApi: the files pane's view of a session's working directories (harness.agent.v1 files RPCs,
// proto/harness/agent/v1/files.proto). Two implementations: connect.ts (a real client) and mock.ts (a small fake
// project, for the mock transport).
//
// Paths are absolute, or relative to the session's cwd; they must stay inside the session's working directories
// (the client refuses others). Sessions of other machines cannot be browsed (FAILED_PRECONDITION).

// A, M, D, U(ntracked), R(enamed), C(onflicted); '' when unchanged or outside a repository.
export type GitStatus = '' | 'A' | 'M' | 'D' | 'U' | 'R' | 'C'

export type EntryKind = 'file' | 'dir' | 'symlink' | 'other'

// The client's limits (packages/client/server/filebrowser.go).
export const FILE_LIMITS = {
  // Text beyond this is cut (FileContent.truncated).
  text: 2 << 20,
  // Larger images come back without data (FileContent.tooLarge).
  image: 10 << 20,
  // Entries per directory listing (DirListing.truncated).
  entries: 5000
} as const

export interface FileRoot {
  // Absolute; the session's cwd first, then its additional directories.
  path: string
  name: string
  // The repository's top level when the root is inside a git repository.
  gitRoot: string
}

export interface FileEntry {
  name: string
  // Absolute.
  path: string
  kind: EntryKind
  // A symlink to a file or directory inside the working directories (kind is the target's kind then).
  symlink: boolean
  size: number
  // RFC 3339; empty for deleted entries.
  modifiedAt: string
  git: GitStatus
  // Matched by .gitignore, or .git / node_modules: listed only with showIgnored.
  ignored: boolean
  // A tracked file deleted from the working tree.
  deleted: boolean
}

export interface DirListing {
  path: string
  // Directories first, then by name.
  entries: FileEntry[]
  truncated: boolean
}

export interface FileContent {
  path: string
  size: number
  modifiedAt: string
  // Changes when the file does; pass it back as ifNoneMatch to skip an unchanged file.
  etag: string
  // The file still matches ifNoneMatch: nothing else is set.
  notModified: boolean
  mime: string
  binary: boolean
  // UTF-8 text when not binary, possibly cut at the limit (truncated).
  text: string
  // An image's bytes (mime image/*, not SVG: SVG is text).
  data?: Uint8Array
  truncated: boolean
  // No content: the file is above the limit for its kind.
  tooLarge: boolean
}

export interface GitStatusMap {
  // The repository's top level; empty outside a repository.
  root: string
  // Changed paths, absolute (an untracked directory stands for everything in it).
  files: Record<string, GitStatus>
  truncated: boolean
}

export interface FilesApi {
  // The session's working directories.
  roots(sessionId: string): Promise<FileRoot[]>
  // One directory (empty: the session's cwd).
  list(sessionId: string, path: string, opts?: { showIgnored?: boolean }): Promise<DirListing>
  // A file within the limits; with ifNoneMatch, an unchanged file comes back as notModified.
  read(sessionId: string, path: string, opts?: { maxBytes?: number, ifNoneMatch?: string }): Promise<FileContent>
  // The changed paths of the repository containing path (empty: the session's cwd).
  gitStatus(sessionId: string, path?: string): Promise<GitStatusMap>
  // File names in the session's cwd matching query (fuzzy, best first), relative to the cwd (SuggestFiles).
  search(sessionId: string, query: string, limit: number): Promise<string[]>
}
