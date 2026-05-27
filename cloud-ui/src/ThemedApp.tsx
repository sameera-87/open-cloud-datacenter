import { useMemo, useState } from 'react';
import { FluentProvider } from '@fluentui/react-components';
import { RouterProvider } from 'react-router-dom';
import { ApiProvider } from './api/ApiContext';
import { AuthProvider } from './auth/AuthContext';
import { ConfirmDialogProvider } from './components/ConfirmDialog';
import { buildRouter } from './router';
import { wso2DarkTheme, wso2LightTheme } from './theme/themes';

export default function ThemedApp() {
  const [dark, setDark] = useState(false);
  const theme = dark ? wso2DarkTheme : wso2LightTheme;
  const router = useMemo(() => buildRouter({ isDark: dark, onToggleDark: setDark }), [dark]);

  return (
    <FluentProvider theme={theme}>
      <AuthProvider>
        <ApiProvider>
          <ConfirmDialogProvider>
            <RouterProvider router={router} />
          </ConfirmDialogProvider>
        </ApiProvider>
      </AuthProvider>
    </FluentProvider>
  );
}
