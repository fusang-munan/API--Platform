import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { useAuthStore } from '../store/auth'
import Login from '../views/auth/Login.vue'
import Register from '../views/auth/Register.vue'
import Recovery from '../views/auth/Recovery.vue'
import TeacherActivation from '../views/auth/TeacherActivation.vue'
import StudentKeys from '../views/student/StudentKeys.vue'
import StudentModels from '../views/student/StudentModels.vue'
import StudentUsage from '../views/student/StudentUsage.vue'
import MentorProjects from '../views/mentor/MentorProjects.vue'
import MentorReviews from '../views/mentor/MentorReviews.vue'
import MentorCallReview from '../views/mentor/MentorCallReview.vue'
import TeacherStudents from '../views/teacher/TeacherStudents.vue'
import TeacherOrganizations from '../views/teacher/TeacherOrganizations.vue'
import TeacherProjects from '../views/teacher/TeacherProjects.vue'
import TeacherProviders from '../views/teacher/TeacherProviders.vue'
import TeacherModels from '../views/teacher/TeacherModels.vue'
import TeacherProjectSpend from '../views/teacher/TeacherProjectSpend.vue'
import AppLayout from '../layouts/AppLayout.vue'

export const routes: RouteRecordRaw[] = [
  { path: '/login', component: Login, meta: { public: true } },
  { path: '/register', component: Register, meta: { public: true } },
  { path: '/recovery', component: Recovery, meta: { public: true } },
  { path: '/teacher/activate', component: TeacherActivation, meta: { public: true } },
  { path: '/', component: AppLayout, children: [
    { path: '', component: { template: '<div />' } },
    { path: 'student/api-keys', name: 'student-api-keys', component: StudentKeys, meta: { role: 'student', title: '申请密钥' } },
    { path: 'student/models', name: 'student-models', component: StudentModels, meta: { role: 'student', title: '模型广场' } },
    { path: 'student/usage', name: 'student-usage', component: StudentUsage, meta: { role: 'student', title: '个人用量' } },
    { path: 'mentor/projects', name: 'mentor-projects', component: MentorProjects, meta: { role: 'mentor', title: '项目管理' } },
    { path: 'mentor/reviews', name: 'mentor-reviews', component: MentorReviews, meta: { role: 'mentor', title: '密钥管理' } },
    { path: 'mentor/call-review', name: 'mentor-call-review', component: MentorCallReview, meta: { role: 'mentor', title: '调用审查' } },
    { path: 'mentor/call-logs', redirect: '/mentor/call-review' },
    { path: 'mentor/input-monitor', redirect: '/mentor/call-review' },
    { path: 'teacher/students', name: 'teacher-students', component: TeacherStudents, meta: { role: 'teacher', title: '学生管理' } },
    { path: 'teacher/organizations', name: 'teacher-organizations', component: TeacherOrganizations, meta: { role: 'teacher', title: '组织管理' } },
    { path: 'teacher/project-budgets', redirect: '/teacher/projects' },
    { path: 'teacher/projects', name: 'teacher-projects', component: TeacherProjects, meta: { role: 'teacher', title: '项目管理' } },
    { path: 'teacher/project-spend', name: 'teacher-project-spend', component: TeacherProjectSpend, meta: { role: 'teacher', title: '项目监控' } },
    { path: 'teacher/providers', name: 'teacher-providers', component: TeacherProviders, meta: { role: 'teacher', title: '供应商管理' } },
    { path: 'teacher/models', name: 'teacher-models', component: TeacherModels, meta: { role: 'teacher', title: '模型管理' } },
  ] }
]

const router = createRouter({ history: createWebHistory(), routes })

router.beforeEach(async (to) => {
  const auth = useAuthStore()
  await auth.bootstrap()
  if (to.meta.public) return auth.isAuthenticated ? '/' : true
  if (!auth.isAuthenticated) return { path: '/login', query: { redirect: to.fullPath } }
  if (to.path === '/') {
    const role = auth.identity ?? auth.user?.role
    return role === 'teacher' ? '/teacher/projects' : role === 'mentor' ? '/mentor/reviews' : '/student/api-keys'
  }
  const role = to.meta.role as 'student' | 'mentor' | 'teacher' | undefined
  if (role && !auth.canAccess(role)) return '/'
  return true
})
export default router
