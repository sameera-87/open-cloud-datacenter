import { makeStyles, tokens } from '@fluentui/react-components';
import { Outlet } from 'react-router-dom';
import SideNav from './SideNav';
import TopBar from './TopBar';

const useStyles = makeStyles({
  root: {
    display: 'grid',
    gridTemplateRows: '48px 1fr',
    minHeight: '100vh',
    backgroundColor: tokens.colorNeutralBackground2,
  },
  body: {
    display: 'grid',
    gridTemplateColumns: '224px 1fr',
    minHeight: 0,
  },
  main: {
    overflow: 'auto',
    backgroundColor: tokens.colorNeutralBackground2,
  },
});

interface AppShellProps {
  isDark: boolean;
  onToggleDark: (next: boolean) => void;
}

export default function AppShell({ isDark, onToggleDark }: AppShellProps) {
  const styles = useStyles();
  return (
    <div className={styles.root}>
      <TopBar isDark={isDark} onToggleDark={onToggleDark} />
      <div className={styles.body}>
        <SideNav />
        <main className={styles.main}>
          <Outlet />
        </main>
      </div>
    </div>
  );
}
