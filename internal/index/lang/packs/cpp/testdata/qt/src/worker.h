#pragma once

#include <QThread>

class Exporter : public QThread
{
    Q_OBJECT
protected:
    void run() override;
};
