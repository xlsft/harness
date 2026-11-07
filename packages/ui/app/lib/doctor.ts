// The checks of `harness-client doctor` as Settings > About shows them (AgentService.RunDoctor). The client runs them; this
// only maps its answer.
import { DoctorStatus, type RunDoctorResponse } from '~/gen/harness/agent/v1/doctor_pb'

export type DoctorState = 'pass' | 'warn' | 'fail' | 'skip'

export interface DoctorCheckVM {
  id: string
  title: string
  status: DoctorState
  detail: string
  hint: string
}

export interface DoctorReport {
  status: DoctorState
  clientVersion: string
  checks: DoctorCheckVM[]
}

export function doctorState(s: DoctorStatus): DoctorState {
  switch (s) {
    case DoctorStatus.PASS: return 'pass'
    case DoctorStatus.WARN: return 'warn'
    case DoctorStatus.FAIL: return 'fail'
    default: return 'skip'
  }
}

export function doctorReport(r: RunDoctorResponse): DoctorReport {
  return {
    status: doctorState(r.status),
    clientVersion: r.clientVersion,
    checks: r.checks.map(c => ({ id: c.id, title: c.title, status: doctorState(c.status), detail: c.detail, hint: c.hint }))
  }
}
