import { fireEvent, render, screen, cleanup } from '@testing-library/react'
import { afterEach, expect, test, vi } from 'vitest'
import AppPageHeader from './AppPageHeader'
import { createPageActions } from '../../lib/page-v2'
vi.mock('react-i18next',()=>({useTranslation:()=>({t:(key:string)=>key})}))
afterEach(cleanup)
test('keeps signed identity visible and renders page strings as text with common Ant actions',()=>{
  const execute=vi.fn(), actions=createPageActions({execute,publish:()=>{}})
  actions.setPage({title:'<img src=x>',description:'Recent records',actions:[{id:'save',label:'Save',variant:'primary',disabled:false,loading:false}]})
  const view=render(<AppPageHeader name="Signed application" icon={<span>Icon</span>} onBack={()=>{}} state={actions.snapshot()}/>)
  expect(screen.getByRole('heading',{name:'<img src=x>'})).toBeTruthy()
  expect(screen.getByText('Signed application')).toBeTruthy()
  expect(screen.getByText('Recent records')).toBeTruthy()
  expect(view.container.querySelector('img')).toBeNull()
  fireEvent.click(screen.getByRole('button',{name:'Save'}))
  expect(execute).toHaveBeenCalledTimes(1)
  view.rerender(<AppPageHeader name="Signed application" icon={null} onBack={()=>{}} state={actions.snapshot()}/>)
  expect((screen.getByRole('button',{name:'Save'}) as HTMLButtonElement).disabled).toBe(true)
  actions.close()
})
test('falls back to signed name with no page and exposes a fixed uncertain result',()=>{
  const actions=createPageActions({execute:()=>{throw Error('secret')},publish:()=>{}})
  actions.setPage({title:'',description:'',actions:[{id:'delete',label:'Delete',variant:'danger',disabled:false,loading:false}]})
  actions.snapshot()!.invoke('delete')
  const view=render(<AppPageHeader name="Signed application" icon={null} onBack={()=>{}} state={actions.snapshot()}/>)
  expect(screen.getByRole('heading',{name:'Signed application'})).toBeTruthy()
  expect(screen.getByRole('alert').textContent).toContain('apps.v2ActionUncertain')
  expect(view.container.textContent).not.toContain('secret')
  actions.close()
})
