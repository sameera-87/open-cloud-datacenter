import { makeStyles, shorthands, tokens } from '@fluentui/react-components';
import {
  Apps20Regular,
  Box20Regular,
  CloudCube20Regular,
  Database20Regular,
  Globe20Regular,
  History20Regular,
  Image20Regular,
  Key20Regular,
  LockClosed20Regular,
  NetworkCheck20Regular,
  PersonShield20Regular,
  Server20Regular,
  ShieldKeyhole20Regular,
  ShieldPerson20Regular,
} from '@fluentui/react-icons';
import { NavLink, useParams } from 'react-router-dom';
import type { ReactNode } from 'react';
import { useActiveProject } from '../hooks/useActiveProject';

const useStyles = makeStyles({
  nav: {
    width: '224px',
    flexShrink: 0,
    backgroundColor: tokens.colorNeutralBackground1,
    ...shorthands.borderRight('1px', 'solid', tokens.colorNeutralStroke2),
    padding: tokens.spacingHorizontalS,
    overflowY: 'auto',
  },
  scopeDivider: {
    marginTop: tokens.spacingVerticalL,
    marginBottom: tokens.spacingVerticalS,
    marginLeft: tokens.spacingHorizontalS,
    marginRight: tokens.spacingHorizontalS,
    borderTop: `1px solid ${tokens.colorNeutralStroke2}`,
  },
  scopeBanner: {
    padding: `${tokens.spacingVerticalS} ${tokens.spacingHorizontalS} 0`,
    fontSize: tokens.fontSizeBase100,
    color: tokens.colorNeutralForeground3,
    fontStyle: 'italic',
  },
  group: { padding: `${tokens.spacingVerticalM} ${tokens.spacingHorizontalS} ${tokens.spacingVerticalXS}` },
  groupLabel: {
    fontSize: tokens.fontSizeBase100,
    color: tokens.colorNeutralForeground3,
    textTransform: 'uppercase',
    letterSpacing: '0.04em',
    fontWeight: 600,
  },
  link: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalS,
    padding: `${tokens.spacingVerticalS} ${tokens.spacingHorizontalS}`,
    borderRadius: tokens.borderRadiusMedium,
    color: tokens.colorNeutralForeground2,
    textDecoration: 'none',
    fontSize: tokens.fontSizeBase300,
    transition: 'background-color 0.1s',
  },
  linkHover: { backgroundColor: tokens.colorNeutralBackground1Hover },
  linkActive: {
    backgroundColor: tokens.colorBrandBackground2,
    color: tokens.colorBrandForeground1,
    fontWeight: 600,
  },
});

interface NavItem {
  label: string;
  to: string;
  icon: ReactNode;
}

// Items scoped to the project (under /projects/:projectId/)
const projectGroups: { label: string; items: NavItem[] }[] = [
  {
    label: 'Overview',
    items: [
      { label: 'Dashboard', to: 'dashboard', icon: <Apps20Regular /> },
      { label: 'Activity', to: 'activity', icon: <History20Regular /> },
    ],
  },
  {
    label: 'Compute',
    items: [
      { label: 'Virtual machines', to: 'vms', icon: <Server20Regular /> },
      { label: 'Bastions', to: 'bastions', icon: <PersonShield20Regular /> },
      { label: 'Clusters', to: 'clusters', icon: <CloudCube20Regular /> },
    ],
  },
  {
    label: 'Networking',
    items: [
      { label: 'Virtual networks', to: 'vnets', icon: <NetworkCheck20Regular /> },
      { label: 'Security groups', to: 'nsgs', icon: <ShieldKeyhole20Regular /> },
      { label: 'Private DNS', to: 'dns', icon: <Globe20Regular /> },
    ],
  },
  {
    label: 'Security',
    items: [
      { label: 'Key vaults', to: 'keyvaults', icon: <LockClosed20Regular /> },
      { label: 'Service accounts', to: 'service-accounts', icon: <Box20Regular /> },
    ],
  },
  {
    label: 'Managed services',
    items: [
      { label: 'Databases', to: 'databases', icon: <Database20Regular /> },
    ],
  },
];

// Items scoped to the tenant (not under /projects/)
const tenantGroups: { label: string; items: NavItem[] }[] = [
  {
    label: 'Catalog',
    items: [
      { label: 'Images', to: 'images', icon: <Image20Regular /> },
      { label: 'SSH keys', to: 'keys', icon: <Key20Regular /> },
    ],
  },
  {
    label: 'Tenant',
    items: [
      { label: 'Access control', to: 'iam', icon: <ShieldPerson20Regular /> },
    ],
  },
];

export default function SideNav() {
  const styles = useStyles();
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();

  const tenantBase = tenantId ? `/tenants/${tenantId}` : '';
  const projectBase = tenantId && projectId ? `/tenants/${tenantId}/projects/${projectId}` : null;

  return (
    <aside className={styles.nav}>
      {/* Project-scoped items — only render when inside a project route */}
      {projectBase &&
        projectGroups.map((g) => (
          <div key={g.label}>
            <div className={styles.group}>
              <span className={styles.groupLabel}>{g.label}</span>
            </div>
            {g.items.map((item) => (
              <NavLink
                key={item.to}
                to={`${projectBase}/${item.to}`}
                className={({ isActive }) =>
                  `${styles.link} ${isActive ? styles.linkActive : styles.linkHover}`
                }
              >
                {item.icon}
                <span>{item.label}</span>
              </NavLink>
            ))}
          </div>
        ))}

      {/* Tenant-scoped items — always show when inside a tenant.
          When inside a project, show a divider + scope banner so users
          know clicking these leaves the project context. */}
      {tenantBase && projectBase && <div className={styles.scopeDivider} />}
      {tenantBase && projectBase && (
        <div className={styles.scopeBanner}>Tenant scope — leaves project context</div>
      )}
      {tenantBase &&
        tenantGroups.map((g) => (
          <div key={g.label}>
            <div className={styles.group}>
              <span className={styles.groupLabel}>{g.label}</span>
            </div>
            {g.items.map((item) => (
              <NavLink
                key={item.to}
                to={`${tenantBase}/${item.to}`}
                className={({ isActive }) =>
                  `${styles.link} ${isActive ? styles.linkActive : styles.linkHover}`
                }
              >
                {item.icon}
                <span>{item.label}</span>
              </NavLink>
            ))}
          </div>
        ))}
    </aside>
  );
}
