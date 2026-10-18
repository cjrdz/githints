#include "mainwindow.h"
#include "../worker.h"

MainWindow::MainWindow(QWidget *parent)
    : QMainWindow(parent),
      model_(new TodoModel(this))
{
    setWindowTitle(R"(class Fake : public QWidget {)");
}

MainWindow::~MainWindow() = default;

void MainWindow::addTodo()
{
    model_->add(QStringLiteral("new"));
}
