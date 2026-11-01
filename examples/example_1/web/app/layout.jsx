import './style.css';

export const metadata = { title: 'Todo · Fugato Example 1' };

export default function Layout({ children }) {
  // Browser extensions can add attributes such as data-kantu before hydration.
  return <html lang="en" suppressHydrationWarning><body>{children}</body></html>;
}
