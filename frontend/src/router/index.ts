import {
  createRouter,
  createWebHistory,
  type RouteRecordRaw,
  type Router,
  type RouterHistory,
} from 'vue-router'

// Ids are positive integers of up to 15 digits, which are always safe integers.
const idProps = (route: { params: Record<string, unknown> }) => ({ id: Number(route.params.id) })

export const routes: RouteRecordRaw[] = [
  { path: '/', redirect: { name: 'books' } },
  {
    path: '/books',
    name: 'books',
    component: () => import('@/views/BookListView.vue'),
  },
  {
    path: '/books/new',
    name: 'book-new',
    component: () => import('@/views/BookFormView.vue'),
  },
  {
    path: '/books/:id([1-9][0-9]{0,14})',
    name: 'book',
    component: () => import('@/views/BookDetailView.vue'),
    props: idProps,
  },
  {
    path: '/books/:id([1-9][0-9]{0,14})/edit',
    name: 'book-edit',
    component: () => import('@/views/BookFormView.vue'),
    props: idProps,
  },
  {
    path: '/:pathMatch(.*)*',
    name: 'not-found',
    component: () => import('@/views/NotFoundView.vue'),
  },
]

export function createAppRouter(history: RouterHistory = createWebHistory(import.meta.env.BASE_URL)): Router {
  return createRouter({ history, routes })
}

export default createAppRouter()
