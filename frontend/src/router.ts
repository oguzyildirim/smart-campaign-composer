import { createRouter, createWebHistory, type RouteRecordRaw, type RouterHistory } from 'vue-router'
import CampaignListView from './views/CampaignListView.vue'
import NewCampaignView from './views/NewCampaignView.vue'
import CampaignReviewView from './views/CampaignReviewView.vue'

export const routes: RouteRecordRaw[] = [
  { path: '/', redirect: '/campaigns' },
  { path: '/campaigns', name: 'campaigns', component: CampaignListView },
  { path: '/campaigns/new', name: 'new-campaign', component: NewCampaignView },
  { path: '/campaigns/:id', name: 'campaign', component: CampaignReviewView, props: true },
  { path: '/:pathMatch(.*)*', redirect: '/campaigns' },
]

// makeRouter takes the history so tests can use createMemoryHistory.
export function makeRouter(history: RouterHistory = createWebHistory()) {
  return createRouter({ history, routes })
}
