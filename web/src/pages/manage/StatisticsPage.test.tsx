import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { it, expect, vi } from 'vitest'
import StatisticsPage from './StatisticsPage'
const {get,t} = vi.hoisted(() => ({get:vi.fn(),t:(key:string)=>key}))
vi.mock('./DashboardChart',()=>({default:({rows}:{rows:{name:string;value:number}[]})=><div data-testid="statistics-chart">{rows.map(row=><span key={row.name}>{row.name}</span>)}</div>}))
vi.mock('../../api/client', () => ({api:{get},errText:()=> 'Failed'}))
vi.mock('react-i18next', () => ({useTranslation:()=>({t})}))
it('queries statistics independently and exports the applied filters across pages',async()=>{
 get.mockResolvedValue({items:[{label:'Research',count:12}],total:51,records:400})
 render(<App><StatisticsPage /></App>)
 expect(await screen.findByText('Research')).toBeTruthy()
 expect(get.mock.calls[0][0]).toContain('/api/admin/statistics?dataset=reports')
 const link=screen.getByRole('link',{name:/statistics.download/})
 expect(link.getAttribute('href')).toContain('format=csv')
 fireEvent.change(screen.getByRole('searchbox'),{target:{value:'Research'}})
 fireEvent.click(screen.getByRole('button',{name:/statistics.apply/}))
 await waitFor(()=> expect(get.mock.lastCall![0]).toContain('q=Research'))
 expect(screen.getByRole('link',{name:/statistics.download/}).getAttribute('href')).toContain('q=Research')
})

it('shows a chart by default and switches to the independently paginated table', async () => {
 get.mockClear().mockResolvedValue({items:[{label:'Research',count:12}],total:30,records:80,other:68})
 render(<App><StatisticsPage /></App>)
 expect(await screen.findByTestId('statistics-chart')).toBeTruthy()
 expect(get.mock.calls[0][0]).toContain('view=chart')
 expect(screen.getByText('manage.dashboard.otherKinds')).toBeTruthy()
 fireEvent.click(screen.getByText('statistics.table'))
 expect(await screen.findByRole('table')).toBeTruthy()
 expect(get.mock.lastCall![0]).not.toContain('view=chart')
})

it('asks for a coarser interval instead of plotting a truncated date series', async () => {
 get.mockResolvedValue({items:[],records:800,total:401,too_many_points:true})
 render(<App><StatisticsPage /></App>)
 expect(await screen.findByText('statistics.tooManyPoints')).toBeTruthy()
 expect(screen.queryByTestId('statistics-chart')).toBeNull()
})
