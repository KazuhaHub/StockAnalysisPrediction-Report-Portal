import { describe, expect, it } from 'vitest'
import type { AppAccessOU } from '../../api/types'
import { serviceStatus } from './appsAdminModel'

const acc = (ou: number, effective: boolean): AppAccessOU => ({ ou, own: null, effective, source_ou: 0, inherited: false })

describe('serviceStatus', () => {
  it('is "all" when every OU is open, and also when there is nothing to count', () => {
    expect(serviceStatus([acc(1, true), acc(2, true)])).toEqual({ kind: 'all', open: 2, total: 2 })
    expect(serviceStatus([])).toEqual({ kind: 'all', open: 0, total: 0 })
  })
  it('is "none" when every OU is closed', () => {
    expect(serviceStatus([acc(1, false), acc(2, false)])).toEqual({ kind: 'none', open: 0, total: 2 })
  })
  it('is "some" with the open count otherwise', () => {
    expect(serviceStatus([acc(1, true), acc(2, false), acc(3, true)])).toEqual({ kind: 'some', open: 2, total: 3 })
  })
})
